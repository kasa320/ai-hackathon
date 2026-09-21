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

// PreparationData は参加条件の輪読用の型。参加できる時間帯と、担当の辞退。
//
// 輪読は日程を決めてから範囲を読んでくるので、準備状況（読んできた範囲・説明できる範囲）は聞かない。
// 説明の担当は AI が割り振り、割り振られた本人が引き受けるかを答える。
type PreparationData struct {
	// DeclinedPresentation は本人が今回の説明の担当を辞退したこと（「担当を辞退する」）。
	// true の人には担当を割り振らない。
	DeclinedPresentation bool `json:"declined_presentation"`
	// UnavailableDates は出られない日（YYYY-MM-DD、JST）。日時がまだ決まっていない回で使う。
	// 参加可能時間の申告より優先する終日不可の例外。
	UnavailableDates []string              `json:"unavailable_dates"`
	Schedule         *ScheduleAvailability `json:"schedule,omitempty"`
}

// ScheduleAvailability contains only the member's confirmed scheduling constraints.
// Times are JST; free-form explanations never cross this boundary.
type ScheduleAvailability struct {
	Status        string         `json:"status"`
	WeeklyWindows []WeeklyWindow `json:"weekly_windows"`
	DateWindows   []DateWindow   `json:"date_windows"`
	// MaxDurationMinutes は本人が自分から言った1回の参加時間の上限。0 は指定なし（聞かない）。
	// 会の長さより短い時間帯は、上限がなくても候補にならない。
	MaxDurationMinutes int `json:"max_duration_minutes"`
}

type WeeklyWindow struct {
	Weekday int    `json:"weekday"`
	Start   string `json:"start"`
	End     string `json:"end"`
}

type DateWindow struct {
	Date  string `json:"date"`
	Start string `json:"start"`
	End   string `json:"end"`
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

// PlanData は coord.Proposal.Data の輪読用の型。今回の範囲と進行表、決まっていなければ開催日時。
type PlanData struct {
	CoveredSectionIDs  []string     `json:"covered_section_ids"`
	DeferredSectionIDs []string     `json:"deferred_section_ids"`
	Agenda             []AgendaItem `json:"agenda"`
	// StartsAt は案が決める開催日時（RFC 3339）。日時が決まっている回では空。
	StartsAt string `json:"starts_at,omitempty"`
	// Frequency は期間全体の進め方を1行で書いたもの（例：週1回60分・全8回）。説明用で、判断には使わない。
	Frequency string `json:"frequency,omitempty"`
}

// 入力の制限（docs/data-structure.md）。
const (
	maxBookTitleLen    = 100
	maxSectionTitleLen = 200
	maxSections        = 100
	maxAgendaItems     = 20
	maxIDLen           = 128
	maxUnavailableDays = 60
	maxFrequencyLen    = 100
	// 同じ人を代役にできる連続回数の上限。3回連続を禁止する。
	maxConsecutiveSubstitutes = 2
)
