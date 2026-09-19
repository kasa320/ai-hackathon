package reading

// 以下は輪読固有データの初期型。保存テーブルや公開APIの確定仕様ではない。

type Section struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type Preparation struct {
	MemberID               string   `json:"member_id"`
	PreparedSectionIDs     []string `json:"prepared_section_ids"`
	ExplainableSectionIDs  []string `json:"explainable_section_ids"`
	MaxPresentationMinutes int      `json:"max_presentation_minutes"`
}

// State は coord.Snapshot.Data の輪読用の型。
type State struct {
	BookTitle           string        `json:"book_title"`
	Sections            []Section     `json:"sections"`
	CompletedSectionIDs []string      `json:"completed_section_ids"`
	PlannedSectionIDs   []string      `json:"planned_section_ids"`
	Preparations        []Preparation `json:"preparations"`
}

type AgendaItem struct {
	SectionIDs  []string `json:"section_ids"`
	Activity    string   `json:"activity"`
	PresenterID string   `json:"presenter_id,omitempty"`
	Minutes     int      `json:"minutes"`
}

// Plan は coord.Proposal.Data の輪読用の型。
type Plan struct {
	Agenda             []AgendaItem `json:"agenda"`
	DeferredSectionIDs []string     `json:"deferred_section_ids"`
}
