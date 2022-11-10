package outputgraph

import (
	"fmt"
	"github.com/streamingfast/substreams/manifest"
	pbsubstreams "github.com/streamingfast/substreams/pb/sf/substreams/v1"
)

// TODO(abourget): rename to `outputmods.Graph` perhaps?
//  * `outputgraph` isn't exact, they are output modules, and it's a graph of those output modules.
//  * once in `outputmods`, having `OutputModulesGraph` becomes redundant.
type OutputModulesGraph struct {
	request *pbsubstreams.Request

	// required stores to be processed, either because requested directly
	// or ancestor to a requested module
	stores []*pbsubstreams.Module

	// TODO(abourget): populate with those mapper output, that adds a layer of
	// scheduling in addition to `storeModules`.
	// outputMapperModules
	requestedMappers []*pbsubstreams.Module

	schedulableModules []*pbsubstreams.Module // stores and output mappers needed to execute to produce output for all `output_modules`.

	allModules       []*pbsubstreams.Module // subset of request.Modules, needed for any `OutputModules`.
	requestedOutputs []*pbsubstreams.Module // modules requested in `OutputModules`
	outputModuleMap  map[string]bool

	schedulableAncestorsMap map[string][]string // modules that are ancestors (therefore dependencies) of a given module

	moduleHashes *manifest.ModuleHashes
}

func (t *OutputModulesGraph) RequestedMapModules() []*pbsubstreams.Module { return t.requestedMappers }
func (t *OutputModulesGraph) Stores() []*pbsubstreams.Module              { return t.stores }
func (g *OutputModulesGraph) AllModules() []*pbsubstreams.Module          { return g.allModules }
func (t *OutputModulesGraph) IsOutputModule(name string) bool             { return t.outputModuleMap[name] }
func (t *OutputModulesGraph) OutputMap() map[string]bool                  { return t.outputModuleMap }
func (t *OutputModulesGraph) ModuleHashes() *manifest.ModuleHashes        { return t.moduleHashes }

func NewOutputModuleGraph(request *pbsubstreams.Request) (out *OutputModulesGraph, err error) {
	outMap := make(map[string]bool)
	for _, name := range request.OutputModules {
		outMap[name] = true
	}
	out = &OutputModulesGraph{
		request:         request,
		outputModuleMap: outMap,
	}
	if err := out.computeGraph(); err != nil {
		return nil, fmt.Errorf("module graph: %w", err)
	}

	return out, nil
}

func (t *OutputModulesGraph) computeGraph() error {
	graph, err := manifest.NewModuleGraph(t.request.Modules.Modules)
	if err != nil {
		return fmt.Errorf("compute graph: %w", err)
	}

	processModules, err := graph.ModulesDownTo(t.request.OutputModules)
	if err != nil {
		return fmt.Errorf("building execution moduleGraph: %w", err)
	}
	t.allModules = processModules
	t.hashModules(graph)

	storeModules, err := graph.StoresDownTo(t.request.OutputModules)
	if err != nil {
		return fmt.Errorf("stores down: %w", err)
	}
	t.stores = storeModules

	t.requestedOutputs = computeOutputModules(t.allModules, t.outputModuleMap)
	t.requestedMappers = computeRequestedMappers(t.allModules, t.outputModuleMap)
	t.schedulableModules = computeSchedulableModules(storeModules, t.requestedMappers)

	ancestorsMap, err := computeSchedulableAncestors(graph, t.schedulableModules)
	if err != nil {
		return fmt.Errorf("computing ancestors: %w", err)
	}
	t.schedulableAncestorsMap = ancestorsMap

	return nil
}

func computeOutputModules(mods []*pbsubstreams.Module, outMap map[string]bool) (out []*pbsubstreams.Module) {
	for _, module := range mods {
		isOutput := outMap[module.Name]
		if isOutput {
			out = append(out, module)
		}
	}
	if len(outMap) != len(out) {
		panic(fmt.Errorf("inconsistent output modules and output modules map: %d and %d", len(out), len(outMap)))
	}
	return
}

func computeRequestedMappers(mods []*pbsubstreams.Module, outMap map[string]bool) (out []*pbsubstreams.Module) {
	for _, module := range mods {
		isOutput := outMap[module.Name]
		if isOutput && module.GetKindMap() != nil {
			out = append(out, module)
		}
	}
	return
}

func computeSchedulableModules(stores []*pbsubstreams.Module, requestedMappers []*pbsubstreams.Module) (out []*pbsubstreams.Module) {
	return append(append(out, stores...), requestedMappers...)
}

func computeSchedulableAncestors(graph *manifest.ModuleGraph, schedulableModules []*pbsubstreams.Module) (out map[string][]string, err error) {
	out = map[string][]string{}
	for _, mod := range schedulableModules {
		ancestors, err := graph.AncestorStoresOf(mod.Name)
		if err != nil {
			return nil, fmt.Errorf("getting ancestor stores for module %s: %w", mod.Name, err)
		}
		out[mod.Name] = moduleNames(ancestors)
	}
	return out, nil
}

func (g *OutputModulesGraph) SchedulableModuleNames() []string {
	return moduleNames(g.schedulableModules)
}

func (g *OutputModulesGraph) AncestorsFrom(moduleName string) []string {
	return g.schedulableAncestorsMap[moduleName]
}

func moduleNames(modules []*pbsubstreams.Module) (out []string) {
	for _, mod := range modules {
		out = append(out, mod.Name)
	}
	return
}

func (t *OutputModulesGraph) hashModules(graph *manifest.ModuleGraph) {
	t.moduleHashes = manifest.NewModuleHashes()
	for _, module := range t.allModules {
		t.moduleHashes.HashModule(t.request.Modules, module, graph)
	}
}

func (t *OutputModulesGraph) ValidateRequestStartBlock(requestStartBlockNum uint64) error {
	for _, module := range t.requestedOutputs {
		if requestStartBlockNum < module.InitialBlock {
			return fmt.Errorf("start block %d smaller than request outputs for module %q with start block %d", requestStartBlockNum, module.Name, module.InitialBlock)
		}
	}
	return nil
}
