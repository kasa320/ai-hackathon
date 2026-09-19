// Package coord は用途別 Playbook の契約と共通の調整処理を担う。
// 用途別パッケージを直接 import せず、cmd/server から実装を受け取る。
// 状態遷移・本人と版の検証・同意管理はここ、用途固有の承認条件は Playbook に置く。
package coord
