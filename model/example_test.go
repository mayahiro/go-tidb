package model_test

import (
	"fmt"

	"github.com/mayahiro/go-tidb/model"
)

type exampleUser struct {
	model.Meta  `tidbgo:"table=users"`
	ID          uint64 `tidbgo:",pk"`
	DisplayName string
	Secret      string         `tidbgo:"-"`
	Orders      []exampleOrder `tidbgo:"has_many,join=ID:UserID"`
}

type exampleOrder struct {
	ID     uint64 `tidbgo:",pk"`
	UserID uint64
}

func ExampleDescribe() {
	metadata, err := model.Describe[exampleUser]()
	if err != nil {
		panic(err)
	}
	fmt.Println(metadata.TableName())
	for _, field := range metadata.Fields() {
		fmt.Println(field.GoName(), field.ColumnName(), field.IsPrimaryKey())
	}
	for _, relation := range metadata.Relations() {
		fmt.Println(relation.GoName(), relation.Kind())
	}
	// Output:
	// users
	// ID id true
	// DisplayName display_name false
	// Orders has-many
}
