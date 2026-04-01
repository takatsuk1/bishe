//go:build reference
// +build reference

package tools

import (
	"context"

	"github.com/olivere/elastic/v7"
	"trpc.group/trpc-go/trpc-go/log"
)

type Es struct {
	Client *elastic.Client
}

func CreateElasticSearchCLient() (*Es, error) {
	client, err := elastic.NewClient(elastic.SetURL("http://localhost:9200"))
	if err != nil {
		log.Fatalf("Error creating the client: %s", err)
	}

	return &Es{
		Client: client,
	}, nil
}

func (es *Es) Search(ctx context.Context, query string) (*elastic.SearchResult, error) {
	q := elastic.NewMatchQuery("content", query)
	searchResult, err := es.Client.Search().
		Index("knowledge_base").Query(q).
		Do(ctx)
	if err != nil {
		log.Errorf("Error executing search: %s", err)
		return nil, err
	}
	if searchResult.TotalHits() == 0 {
		log.Warnf("No results found for query: %s", query)
		return nil, nil
	}
	return searchResult, nil
}
