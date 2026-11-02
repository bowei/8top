package render

import "go.ate.dev/8top/internal/model"

var renderers = map[model.Kind]Renderer{
	model.KindAtespace:        atespaceRenderer{},
	model.KindActor:           actorRenderer{},
	model.KindActorTemplate:   templateRenderer{},
	model.KindTag:             tagRenderer{},
	model.KindEgressPolicy:    egressRenderer{},
	model.KindWorker:          workerRenderer{},
	model.KindActorAssignment: assignmentRenderer{},
	model.KindAccessPolicy:    accessPolicyRenderer{},
	model.KindWorkerPool:      workerPoolRenderer{},
}

// For returns the renderer for kind, or nil if kind is unknown.
func For(kind model.Kind) Renderer {
	return renderers[kind]
}
