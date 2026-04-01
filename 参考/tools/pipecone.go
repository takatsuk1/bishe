//go:build reference
// +build reference

package tools

import "github.com/pinecone-io/go-pinecone/v4/pinecone"

func CreatePineconeConn(apikey string, host string) (*pinecone.IndexConnection, error) {
	pc, err := pinecone.NewClient(pinecone.NewClientParams{
		ApiKey: apikey,
	})
	if err != nil {
		return nil, err
	}
	idxConn, err := pc.Index(pinecone.NewIndexConnParams{Host: host})
	if err != nil {
		return nil, err
	}
	return idxConn, nil
}
