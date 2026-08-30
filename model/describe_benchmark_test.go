package model

import (
	"reflect"
	"testing"
	"time"
)

type descriptorBenchmarkModel struct {
	Meta      `tidbgo:"table=benchmark_models"`
	ID        uint64 `tidbgo:"id,pk"`
	TenantID  uint64
	Email     string `tidbgo:"email_address"`
	CreatedAt time.Time
	UpdatedAt *time.Time
	Payload   []byte
	Ignored   string `tidbgo:"-"`
}

var descriptorBenchmarkSink *Descriptor

func BenchmarkDescribeCached(b *testing.B) {
	if _, err := Describe[descriptorBenchmarkModel](); err != nil {
		b.Fatal(err)
	}

	var descriptor *Descriptor
	var err error
	b.ReportAllocs()
	for b.Loop() {
		descriptor, err = Describe[descriptorBenchmarkModel]()
		if err != nil {
			b.Fatal(err)
		}
	}
	descriptorBenchmarkSink = descriptor
}

func BenchmarkParseDescriptor(b *testing.B) {
	modelType := reflect.TypeFor[descriptorBenchmarkModel]()
	var descriptor *Descriptor
	var err error
	b.ReportAllocs()
	for b.Loop() {
		descriptor, err = parseDescriptor(modelType)
		if err != nil {
			b.Fatal(err)
		}
	}
	descriptorBenchmarkSink = descriptor
}
