package sourceimport

// Project keeps execution facts and capability policy in the management read
// model. Full source identities and expression provenance remain in the durable
// journal; per-record maps are execution evidence rather than page contents.
func Project(result Result) Result {
	result.Batches = append([]Batch{}, result.Batches...)
	for index := range result.Batches {
		result.Batches[index].Mappings = []Mapping{}
	}
	result.Fields = append([]FieldSummary{}, result.Fields...)
	for index := range result.Fields {
		result.Fields[index].Definition = ""
	}
	return result
}
