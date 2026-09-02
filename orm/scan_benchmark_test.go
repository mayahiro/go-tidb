package orm

import (
	"reflect"
	"testing"
	"time"
)

type scanBenchmarkModel struct {
	ID        uint64
	Name      string
	CreatedAt time.Time
	Payload   []byte
}

type noOpScanRow struct{}

func (noOpScanRow) Scan(...any) error { return nil }

var scanBenchmarkSink scanBenchmarkModel

func BenchmarkRowDecoderScan(b *testing.B) {
	plan, err := scanPlanForTest(reflect.TypeFor[scanBenchmarkModel]())
	if err != nil {
		b.Fatal(err)
	}
	decoder := plan.newDecoder()
	row := noOpScanRow{}
	var target scanBenchmarkModel

	b.ReportAllocs()
	for b.Loop() {
		if err := decoder.scan(row, &target); err != nil {
			b.Fatal(err)
		}
	}
	scanBenchmarkSink = target
}
