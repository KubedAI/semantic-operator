// ossiectl: offline tooling for SemanticModel resources.
//
//	ossiectl validate -f model.yaml          validate a SemanticModel CR
//	ossiectl derive   -region us-west-2 -database osi_demo [-out model.yaml]
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
	"github.com/KubedAI/semantic-operator/internal/catalog/glue"
	"github.com/KubedAI/semantic-operator/internal/ossie"
	"github.com/KubedAI/semantic-operator/internal/planner"
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

func cmdDerive(args []string) error {
	fs := flag.NewFlagSet("derive", flag.ExitOnError)
	region := fs.String("region", os.Getenv("AWS_REGION"), "AWS region")
	database := fs.String("database", "", "Glue database")
	modelName := fs.String("model", "derived_model", "Ossie model name")
	crName := fs.String("name", "derived-model", "CR metadata.name")
	namespace := fs.String("namespace", "semantic-system", "CR namespace")
	cat := fs.String("catalog", "iceberg", "StarRocks external catalog name")
	out := fs.String("out", "", "write to this file instead of stdout")
	_ = fs.Parse(args)
	if *database == "" {
		return fmt.Errorf("-database is required")
	}

	src, err := glue.New(context.Background(), *region)
	if err != nil {
		return err
	}
	tables, err := src.ListTables(context.Background(), *database)
	if err != nil {
		return err
	}
	if len(tables) == 0 {
		return fmt.Errorf("no tables found in Glue database %q", *database)
	}

	w := os.Stdout
	if *out != "" {
		f, err := os.Create(*out)
		if err != nil {
			return err
		}
		defer f.Close()
		w = f
	}

	opts := catalog.TemplateOptions{
		CRName:    *crName,
		Namespace: *namespace,
		Catalog:   *cat,
		Database:  *database,
		Model:     *modelName,
	}
	if err := catalog.RenderTemplate(w, opts, tables); err != nil {
		return err
	}
	if *out != "" {
		fmt.Fprintf(os.Stderr, "wrote %s — fill the TODO placeholders, then: ossiectl validate -f %s\n", *out, *out)
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
	os.Stdout.Write(out)
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
	os.Stdout.Write(out)
	return nil
}
