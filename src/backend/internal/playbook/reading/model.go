package reading

// 公開API（docs/data-structure.md）と同じ形の輪読固有データ。

type Section struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type TocSource struct {
	Kind string   `json:"kind"`
	URLs []string `json:"urls"`
}

// SessionData は coord.Snapshot.SessionData の輪読用の型。開催回の教材と範囲。
type SessionData struct {
	BookTitle           string    `json:"book_title"`
	ISBN                *string   `json:"isbn"`
	TocSource           TocSource `json:"toc_source"`
	Sections            []Section `json:"sections"`
	CompletedSectionIDs []string  `json:"completed_section_ids"`
	TargetSectionIDs    []string  `json:"target_section_ids"`
}

// PreparationData は参加条件の輪読用の型。準備状況と担当できる範囲。
type PreparationData struct {
	WillingToPresent       bool     `json:"willing_to_present"`
	PreparedSectionIDs     []string `json:"prepared_section_ids"`
	ExplainableSectionIDs  []string `json:"explainable_section_ids"`
	MaxPresentationMinutes int      `json:"max_presentation_minutes"`
}

// 進行項目の種類。
const (
	ActivityPresentation = "presentation"
	ActivityReview       = "review"
	ActivityDiscussion   = "discussion"
	ActivityJointReading = "joint_reading"
)

type AgendaItem struct {
	ID                string   `json:"id"`
	Activity          string   `json:"activity"`
	SectionIDs        []string `json:"section_ids"`
	PresenterMemberID *string  `json:"presenter_member_id"`
	Minutes           int      `json:"minutes"`
}

// PlanData は coord.Proposal.Data の輪読用の型。今回の範囲と進行表。
type PlanData struct {
	CoveredSectionIDs  []string     `json:"covered_section_ids"`
	DeferredSectionIDs []string     `json:"deferred_section_ids"`
	Agenda             []AgendaItem `json:"agenda"`
}

// 入力の制限（docs/data-structure.md）。
const (
	maxBookTitleLen    = 100
	maxSectionTitleLen = 200
	maxSections        = 100
	maxAgendaItems     = 20
	maxIDLen           = 128
	// 同じ人を代役にできる連続回数の上限。3回連続を禁止する。
	maxConsecutiveSubstitutes = 2
)
