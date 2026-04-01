//go:build reference
// +build reference

package model

import (
	"fmt"
	"strings"
)

type TaskID struct {
	AgentName string
	ID        string
}

func (id *TaskID) Encode() string {
	return fmt.Sprintf("%s:%s", id.AgentName, id.ID)
}

func (id *TaskID) Decode(val string) error {
	splitVals := strings.Split(val, ":")
	if len(splitVals) < 2 {
		return fmt.Errorf("invalid taskID")
	}
	id.AgentName = splitVals[0]
	id.ID = strings.Join(splitVals[1:], ":")
	return nil
}
