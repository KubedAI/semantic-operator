// ossiectl: offline tooling for SemanticModel resources.
//
//	ossiectl validate -f model.yaml          validate a SemanticModel CR
//	ossiectl derive   -database osi_demo [-source engine|glue] [-enrich datahub] [-out model.yaml]
//	                                       scaffold an Ossie SemanticModel from a Glue
//	                                       database (fields populated; metrics/joins/
//	                                       governance emitted as TODO placeholders)
//	ossiectl unwrap   -f cr.yaml             extract the Ossie document from a CR
//	ossiectl wrap     -f ossie.yaml -name x -namespace ns -catalog c -database d
//	                                       wrap an Ossie document into a CR
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"sigs.k8s.io/yaml"

	"github.com/KubedAI/semantic-operator/api/v1alpha1"
	"github.com/KubedAI/semantic-operator/internal/catalog"
	"github.com/KubedAI/semantic-operator/internal/catalog/datahub"
	"github.com/KubedAI/semantic-operator/internal/catalog/glue"
	"github.com/KubedAI/semantic-operator/internal/catalog/infoschema"
	"github.com/KubedAI/semantic-operator/internal/dbclient"
	"github.com/KubedAI/semantic-operator/internal/emitter"
	_ "github.com/KubedAI/semantic-operator/internal/emitter/starrocks"
	_ "github.com/KubedAI/semantic-operator/internal/emitter/trino"
	"github.com/KubedAI/semantic-operator/internal/ossie"
	"github.com/KubedAI/semantic-operator/internal/planner"
	_ "github.com/KubedAI/semantic-operator/internal/starrocks"
	_ "github.com/KubedAI/semantic-operator/internal/trino"
)

// ossieDocument is the top-level Ossie file shape: version + semantic_model list.
type ossieDocument struct {
	Version       string                `json:"version,omitempty"`
	SemanticModel []v1alpha1.OssieModel `json:"semantic_model"`
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "validate":
		err = cmdValidate(os.Args[2:])
	case "derive":
		err = cmdDerive(os.Args[2:])
	case "unwrap":
		err = cmdUnwrap(os.Args[2:])
	case "wrap":
		err = cmdWrap(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: ossiectl <validate|derive|unwrap|wrap> [flags]")
	os.Exit(2)
}

func cmdValidate(args []string) error {
	fs := flag.NewFlagSet("validate", flag.ExitOnError)
	file := fs.String("f", "", "SemanticModel CR file")
	_ = fs.Parse(args)
	if *file == "" {
		return fmt.Errorf("-f is required")
	}
	raw, err := os.ReadFile(*file)
	if err != nil {
		return err
	}
	var cr v1alpha1.SemanticModel
	if err := yaml.UnmarshalStrict(raw, &cr); err != nil {
		return fmt.Errorf("parsing CR: %w", err)
	}
	if err := ossie.ValidateSpec(&cr.Spec); err != nil {
		return err
	}
	if _, err := planner.Compile(&cr.Spec, cr.Namespace, cr.Name); err != nil {
		return fmt.Errorf("compile check: %w", err)
	}
	fmt.Printf("OK: %s (model %s, version %s)\n", *file, cr.Spec.Ossie.Name, planner.SpecVersion(&cr.Spec))
	return nil
}

func cmdDerive(args []string) (err error) {
	fs := flag.NewFlagSet("derive", flag.ExitOnError)
	source := fs.String("source", "glue", "catalog source: glue (AWS SDK) or engine (the live query engine's information_schema; connection from ENGINE_* env; works for any catalog the engine mounts, e.g. Polaris, Hive, Glue)")
	engine := fs.String("engine", envOr("SQL_DIALECT", "starrocks"), "query engine for -source engine: starrocks or trino")
	region := fs.String("region", os.Getenv("AWS_REGION"), "AWS region (glue source)")
	database := fs.String("database", "", "database/schema to scan")
	modelName := fs.String("model", "derived_model", "Ossie model name")
	crName := fs.String("name", "derived-model", "CR metadata.name")
	namespace := fs.String("namespace", "semantic-system", "CR namespace")
	cat := fs.String("catalog", "iceberg", "engine catalog name (also written to spec.connection.catalog)")
	out := fs.String("out", "", "write to this file instead of stdout")
	enrich := fs.String("enrich", "", "metadata source for business meaning: datahub (optional; physical structure always comes from -source)")
	datahubURL := fs.String("datahub-url", os.Getenv("DATAHUB_URL"), "DataHub GMS base URL, e.g. http://datahub-gms.datahub.svc:8080")
	datahubPlatform := fs.String("datahub-platform", "iceberg", "DataHub data platform in dataset URNs (iceberg, trino, hive)")
	datahubPrefix := fs.String("datahub-dataset-prefix", "", "leading component of the DataHub dataset path, when the ingestion source qualifies tables with a catalog (DataHub's trino source names datasets <catalog>.<schema>.<table>, so pass the Trino catalog name; leave empty for the iceberg and hive sources)")
	datahubEnv := fs.String("datahub-env", "PROD", "DataHub fabric type in dataset URNs")
	_ = fs.Parse(args)
	if *database == "" {
		return fmt.Errorf("-database is required")
	}

	ctx := context.Background()
	var src catalog.Source
	switch *source {
	case "glue":
		src, err = glue.New(ctx, *region)
		if err != nil {
			return err
		}
	case "engine":
		d, derr := emitter.Get(*engine)
		if derr != nil {
			return derr
		}
		cfg, cerr := dbclient.EnvConfig()
		if cerr != nil {
			return fmt.Errorf("-source engine needs the engine connection env: %w", cerr)
		}
		db, oerr := dbclient.Open(*engine, cfg)
		if oerr != nil {
			return oerr
		}
		defer func() { _ = db.Close() }()
		src = infoschema.New(db, d, *cat)
	default:
		return fmt.Errorf("unknown -source %q: use glue or engine", *source)
	}

	tables, err := src.ListTables(ctx, *database)
	if err != nil {
		return err
	}
	if len(tables) == 0 {
		return fmt.Errorf("no tables found in database %q", *database)
	}

	// Enrichment decorates the physical scaffold with business meaning. It is
	// deliberately separate from -source: physical truth comes from what the
	// engine sees, meaning comes from the metadata platform.
	var enrichment catalog.Enrichment
	switch *enrich {
	case "":
	case "datahub":
		dh, derr := datahub.New(datahub.Options{
			URL:           *datahubURL,
			Token:         os.Getenv("DATAHUB_TOKEN"),
			Platform:      *datahubPlatform,
			DatasetPrefix: *datahubPrefix,
			Env:           *datahubEnv,
		})
		if derr != nil {
			return derr
		}
		// Report a broken enrichment endpoint rather than silently emitting a
		// bare scaffold: the user explicitly asked for enrichment. Fetch once
		// and interpret the result locally.
		names := make([]string, 0, len(tables))
		for _, t := range tables {
			names = append(names, t.Name)
		}
		meta, perr := dh.DescribeTables(ctx, *database, names)
		if perr != nil {
			return fmt.Errorf("enrich datahub: %w", perr)
		}
		enrichment = catalog.EnrichWith(tables, meta)
		// Zero matches almost always means the URN this derive builds does not
		// address the datasets DataHub holds: a wrong platform, prefix, or env.
		// Fail with the URN that was tried rather than emit a bare scaffold, so
		// the user sees what to correct instead of a silent wall of YAML.
		if len(enrichment.Tables) == 0 {
			return fmt.Errorf("enrich datahub: matched 0 of %d tables\n"+
				"  no DataHub dataset answered the URN this derive built, for example:\n"+
				"    %s\n"+
				"  compare it against the URNs in DataHub and correct -datahub-platform (%q), -datahub-dataset-prefix (%q), or -datahub-env (%q)",
				len(tables), dh.SampleURN(*database, names[0]), *datahubPlatform, *datahubPrefix, *datahubEnv)
		}
		fmt.Fprintf(os.Stderr, "enriched %d/%d tables from DataHub (%d sensitive columns, %d deprecated)\n",
			len(enrichment.Tables), len(tables), len(enrichment.DeniedFields), len(enrichment.DeprecatedTables))
	default:
		return fmt.Errorf("unknown -enrich %q: use datahub", *enrich)
	}

	w := os.Stdout
	if *out != "" {
		f, createErr := os.Create(*out)
		if createErr != nil {
			return createErr
		}
		defer func() {
			if closeErr := f.Close(); err == nil && closeErr != nil {
				err = closeErr
			}
		}()
		w = f
	}

	opts := catalog.TemplateOptions{
		CRName:    *crName,
		Namespace: *namespace,
		Catalog:   *cat,
		Database:  *database,
		Model:     *modelName,
	}
	if err := catalog.RenderTemplateEnriched(w, opts, tables, enrichment); err != nil {
		return err
	}
	if *out != "" {
		_, err := fmt.Fprintf(os.Stderr, "wrote %s — fill the TODO placeholders, then: ossiectl validate -f %s\n", *out, *out)
		if err != nil {
			return err
		}
	}
	return nil
}

func cmdUnwrap(args []string) error {
	fs := flag.NewFlagSet("unwrap", flag.ExitOnError)
	file := fs.String("f", "", "SemanticModel CR file")
	ossieVersion := fs.String("ossie-version", "0.2.0.dev0", "Ossie spec version stamp")
	_ = fs.Parse(args)
	if *file == "" {
		return fmt.Errorf("-f is required")
	}
	raw, err := os.ReadFile(*file)
	if err != nil {
		return err
	}
	var cr v1alpha1.SemanticModel
	if err := yaml.Unmarshal(raw, &cr); err != nil {
		return err
	}
	doc := ossieDocument{Version: *ossieVersion, SemanticModel: []v1alpha1.OssieModel{cr.Spec.Ossie}}
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(out)
	if err != nil {
		return err
	}
	return nil
}

func cmdWrap(args []string) error {
	fs := flag.NewFlagSet("wrap", flag.ExitOnError)
	file := fs.String("f", "", "Ossie document file (version + semantic_model list)")
	name := fs.String("name", "", "CR metadata.name")
	namespace := fs.String("namespace", "semantic-system", "CR namespace")
	cat := fs.String("catalog", "", "StarRocks external catalog name")
	database := fs.String("database", "", "database within the catalog")
	_ = fs.Parse(args)
	if *file == "" || *name == "" || *cat == "" || *database == "" {
		return fmt.Errorf("-f, -name, -catalog, and -database are required")
	}
	raw, err := os.ReadFile(*file)
	if err != nil {
		return err
	}
	var doc ossieDocument
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return err
	}
	if len(doc.SemanticModel) != 1 {
		return fmt.Errorf("expected exactly one semantic_model entry, got %d", len(doc.SemanticModel))
	}
	cr := v1alpha1.SemanticModel{
		Spec: v1alpha1.SemanticModelSpec{
			Connection: v1alpha1.ConnectionSpec{Catalog: *cat, Database: *database},
			Ossie:      doc.SemanticModel[0],
		},
	}
	cr.APIVersion = v1alpha1.GroupVersion.String()
	cr.Kind = "SemanticModel"
	cr.Name = *name
	cr.Namespace = *namespace
	out, err := yaml.Marshal(&cr)
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(out)
	if err != nil {
		return err
	}
	return nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
