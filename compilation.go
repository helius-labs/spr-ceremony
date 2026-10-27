package ceremony

type CompiledCircuit struct {
	Spec        Spec     `json:"spec"`
	R1CS        Artifact `json:"r1cs"`
	Constraints int      `json:"constraints"`
	Domain      uint64   `json:"domain"`
}
type Compilation struct {
	Engine         string            `json:"engine"`
	SourceRevision string            `json:"source_revision"`
	Circuits       []CompiledCircuit `json:"circuits"`
}
