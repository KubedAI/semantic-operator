// Package glue implements catalog.Source against the AWS Glue Data Catalog.
// Credentials come from the default AWS chain, so IRSA works in-cluster with
// no static keys.
package glue

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awsglue "github.com/aws/aws-sdk-go-v2/service/glue"

	"github.com/vara-bonthu/osi-semantic-operator/internal/catalog"
)

// Source lists Iceberg (or any) tables from Glue.
type Source struct {
	client *awsglue.Client
}

// New builds a Glue source for the given region ("" uses the SDK default).
func New(ctx context.Context, region string) (*Source, error) {
	var opts []func(*awsconfig.LoadOptions) error
	if region != "" {
		opts = append(opts, awsconfig.WithRegion(region))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, err
	}
	return &Source{client: awsglue.NewFromConfig(cfg)}, nil
}

// ListTables returns all tables in a Glue database with their columns,
// including Iceberg partition columns.
func (s *Source) ListTables(ctx context.Context, database string) ([]catalog.Table, error) {
	var out []catalog.Table
	p := awsglue.NewGetTablesPaginator(s.client, &awsglue.GetTablesInput{
		DatabaseName: aws.String(database),
	})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, t := range page.TableList {
			ct := catalog.Table{Name: aws.ToString(t.Name)}
			if t.StorageDescriptor != nil {
				for _, c := range t.StorageDescriptor.Columns {
					ct.Columns = append(ct.Columns, catalog.Column{
						Name:    aws.ToString(c.Name),
						Type:    aws.ToString(c.Type),
						Comment: aws.ToString(c.Comment),
					})
				}
			}
			for _, c := range t.PartitionKeys {
				ct.Columns = append(ct.Columns, catalog.Column{
					Name:    aws.ToString(c.Name),
					Type:    aws.ToString(c.Type),
					Comment: aws.ToString(c.Comment),
				})
			}
			out = append(out, ct)
		}
	}
	return out, nil
}
