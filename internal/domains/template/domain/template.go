package domain

type StrategyType string

const (
	StrategyDelete   StrategyType = "delete"
	StrategyUpdate   StrategyType = "update"
	StrategyTruncate StrategyType = "truncate"
)

type RecordType string

const (
	RecordTypeRow  RecordType = "row"
	RecordTypeList RecordType = "list"
)

type Record struct {
	Name string
	Path string
	Type RecordType
}

type Table struct {
	Name     string
	Path     string
	Strategy StrategyType
	Records  []Record
}
