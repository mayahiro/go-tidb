package model_test

import (
	"fmt"

	"github.com/mayahiro/go-tidb/model"
)

type exampleUser struct {
	model.Meta  `tidbgo:"table=users"`
	ID          uint64         `tidbgo:",pk"`
	DisplayName string         `tidbgo:",unique=display_name"`
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
	for _, key := range metadata.UniqueKeys() {
		fmt.Println("unique", key.Name(), key.Fields()[0].GoName())
	}
	// Output:
	// users
	// ID id true
	// DisplayName display_name false
	// Orders has-many
	// unique display_name DisplayName
}
