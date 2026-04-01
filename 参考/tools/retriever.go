//go:build reference
// +build reference

package tools

import (
	"ai/config"
	"context"
	"time"

	"github.com/cloudwego/eino-ext/components/embedding/openai"
	"github.com/cloudwego/eino/components/retriever"
	"github.com/cloudwego/eino/schema"
	"github.com/pinecone-io/go-pinecone/v4/pinecone"
)

type Retriever struct {
	idxConn  *pinecone.IndexConnection
	embedder *openai.Embedder
}

func NewRetriever(ctx context.Context, apikey string, host string) (*Retriever, error) {
	cfg := config.GetMainConfig()
	idxConn, err := CreatePineconeConn(apikey, host)
	if err != nil {
		return nil, err
	}
	embedder, err := openai.NewEmbedder(ctx, &openai.EmbeddingConfig{
		APIKey:  cfg.LLM.APIKey,
		Model:   cfg.LLM.IndexModel,
		BaseURL: cfg.LLM.URL,
		Timeout: 30 * time.Second,
	})

	if err != nil {
		return nil, err
	}
	return &Retriever{
		idxConn:  idxConn,
		embedder: embedder,
	}, nil
}

var defaultTopK = 3

func (r *Retriever) Retrieve(ctx context.Context, query string, opts ...retriever.Option) ([]*schema.Document, error) {
	denseQueryVectors, err := r.embedder.EmbedStrings(ctx, []string{query})
	if err != nil {
		return nil, err
	}
	var denseQueryVector []float32
	for _, q := range denseQueryVectors[0] {
		denseQueryVector = append(denseQueryVector, float32(q))
	}

	// 这是默认配置
	defaultOptions := &retriever.Options{
		TopK: &defaultTopK,
	}
	// 用传入的覆盖默认的
	options := retriever.GetCommonOptions(defaultOptions, opts...)
	denseRes, err := r.idxConn.QueryByVectorValues(ctx, &pinecone.QueryByVectorValuesRequest{
		Vector: denseQueryVector,
		TopK:   uint32(*options.TopK),
		// 不需要返回向量
		IncludeValues:   false,
		IncludeMetadata: true,
	})
	if err != nil {
		return nil, err
	}

	docs := make([]*schema.Document, len(denseRes.Matches))
	for i, match := range denseRes.Matches {
		metadata := match.Vector.Metadata.AsMap()
		docs[i] = &schema.Document{
			ID:      match.Vector.Id,
			Content: metadata["content"].(string),
		}
	}
	return docs, nil
}
