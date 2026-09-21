| 構成 | 要求モデル | 解決モデル | 試行 | 期待通り | 安全停止/拒否 | 失敗 | 所要秒(実測) | 確定額 |
| --- | --- | --- | ---: | ---: | ---: | ---: | --- | --- |
| **計画 E01（合格線60秒）** | | | | | | | | |
| auto | `orcarouter/auto` | `不明` | 3 | 0 | 0 | 3 | 240.2 / 240.2 / 240.2 | 不明 |
| gemini-2.5-flash | `google/gemini-2.5-flash` | `google/gemini-2.5-flash` | 3 | 3 | 0 | 0 | 33.1 / 24.1 / 18.1 | 0.004212 / 0.004576 / 0.006278 |
| gpt-5-mini | `openai/gpt-5-mini` | `openai/gpt-5-mini` | 3 | 3 | 0 | 0 | 18.1 / 18.1 / 21.2 | 0.004608 / 0.004646 / 0.005578 |
| haiku-4.5 | `anthropic/claude-haiku-4.5` | `不明` | 3 | 0 | 0 | 3 | 240.2 / 240.2 / 240.2 | 不明 |
| marunage-plan | `orcarouter/marunage-plan` | `anthropic/claude-sonnet-5` | 4 | 0 | 2 | 2 | 225.1 / 240.2 / 111.1 / 72.2 | 0.102526 / 0.071556 |
| sonnet-5 | `anthropic/claude-sonnet-5` | `anthropic/claude-sonnet-5` | 3 | 0 | 3 | 0 | 60.0 / 66.1 / 75.1 | 0.059348 / 0.057586 / 0.052648 |
| **解釈 E09（合格線20秒）** | | | | | | | | |
| auto | `orcarouter/auto` | `不明` | 3 | 0 | 0 | 3 | 20.0 / 20.0 / 20.0 | 不明 |
| flash | `google/gemini-2.5-flash` | `google/gemini-2.5-flash` | 3 | 1 | 0 | 2 | 35.4 / 35.3 / 3.8 | 0.001218 |
| flash-lite | `google/gemini-2.5-flash-lite` | `google/gemini-2.5-flash-lite` / `不明` | 3 | 1 | 0 | 2 | 20.0 / 1.8 / 30.8 | 0.000186 |
| gpt-5-mini | `openai/gpt-5-mini` | `不明` | 3 | 0 | 0 | 3 | 20.0 / 20.0 / 20.0 | 不明 |
| marunage-extract | `orcarouter/marunage-extract` | `不明` | 3 | 0 | 0 | 3 | 20.0 / 20.0 / 20.0 | 不明 |
| **解釈 E13（合格線40秒）** | | | | | | | | |
| auto | `orcarouter/auto` | `anthropic/claude-opus-5` / `不明` | 3 | 0 | 1 | 2 | 40.0 / 40.0 / 18.3 | 0.046776 |
| flash | `google/gemini-2.5-flash` | `google/gemini-2.5-flash` | 3 | 2 | 1 | 0 | 4.6 / 7.5 / 16.8 | 0.001680 / 0.002988 / 0.007552 |
| flash-lite | `google/gemini-2.5-flash-lite` | `不明` | 3 | 0 | 0 | 3 | 40.0 / 40.0 / 40.0 | 不明 |
| gpt-5-mini | `openai/gpt-5-mini` | `openai/gpt-5-mini` | 3 | 2 | 1 | 0 | 20.3 / 14.5 / 16.3 | 0.005186 / 0.003264 / 0.004288 |
| marunage-extract | `orcarouter/marunage-extract` | `openai/gpt-5-mini` | 3 | 3 | 0 | 0 | 17.7 / 14.6 / 9.5 | 0.003776 / 0.003136 / 0.002494 |
