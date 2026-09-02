package orm

import (
	"context"
	"fmt"

	"github.com/mayahiro/go-tidb/internal/runtimecapture"
	"github.com/mayahiro/go-tidb/model"
)

func runtimeSelectMetadata(ctx context.Context, selection *selectQuery, compiled compiledSelect, terminal string) statementRuntimeMetadata {
	metadata := statementRuntimeMetadata{
		source:   runtimecapture.SourceTypedSelect,
		terminal: terminal,
	}
	if compiled.statement != nil && compiled.statement.scanPlan != nil {
		metadata.model = compiled.statement.scanPlan.modelType.Name()
	}
	if !runtimeCaptureMetadataEnabled(ctx) {
		return metadata
	}
	if selection == nil || compiled.statement == nil || compiled.statement.scanPlan == nil {
		metadata.metadataError = "runtime capture could not describe a compiled SELECT"
		return metadata
	}
	descriptor, err := model.DescribeType(compiled.statement.scanPlan.modelType)
	if err != nil {
		metadata.metadataError = fmt.Errorf("describe runtime SELECT model: %w", err).Error()
		return metadata
	}
	analysis, err := analyzeRelationTopN(descriptor, selection)
	if err != nil {
		metadata.metadataError = fmt.Errorf("analyze runtime relation TopN: %w", err).Error()
		return metadata
	}
	shape, err := buildSelectQueryShape(descriptor, selection, compiled, analysis)
	if err != nil {
		metadata.metadataError = fmt.Errorf("build runtime query shape: %w", err).Error()
		return metadata
	}
	metadata.query = &shape
	return metadata
}

func runtimeTypedSelectMetadata(modelName, terminal string) statementRuntimeMetadata {
	return statementRuntimeMetadata{
		source:   runtimecapture.SourceTypedSelect,
		terminal: terminal,
		model:    modelName,
	}
}

func runtimeTypedMutationMetadata(modelName, terminal string) statementRuntimeMetadata {
	return statementRuntimeMetadata{
		source:   runtimecapture.SourceTypedMutation,
		terminal: terminal,
		model:    modelName,
	}
}

func runtimeRawMetadata(modelName, terminal string) statementRuntimeMetadata {
	return statementRuntimeMetadata{
		source:   runtimecapture.SourceRaw,
		terminal: terminal,
		model:    modelName,
	}
}

func runtimePlanMetadata(metadata statementRuntimeMetadata) statementRuntimeMetadata {
	metadata.source = runtimecapture.SourcePlan
	return metadata
}

func runtimePreloadMetadata(plan *preloadPlan, relationPath string, batch *runtimecapture.Batch) statementRuntimeMetadata {
	return statementRuntimeMetadata{
		source:   runtimecapture.SourcePreload,
		terminal: "preload",
		model:    plan.targetType.Name(),
		relation: relationPath,
		batch:    batch,
	}
}
