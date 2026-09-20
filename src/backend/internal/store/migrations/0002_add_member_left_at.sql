-- メンバーの脱退を記録する。NULL の間は在籍中。
-- 行は消さない：session_members・preparations・tasks から参照されており、
-- 過去の開催回の履歴（誰が担当したか）を残す必要があるため。
ALTER TABLE members ADD COLUMN left_at TEXT;
