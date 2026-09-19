# 冷蔵庫エージェント技術・競合調査

- 確認日: 2026-09-19
- 目的: ESP32を使った冷蔵庫在庫取得と、献立・買い物エージェントの実現可能性を確認する

## 結論

ESP32で実現可能。ただし、ESP32だけで任意の冷蔵庫内の食品を完全認識する構成は避ける。

ESP32-S3は、ドア状態、重量、温湿度などのセンサー取得、カメラ撮影、Wi-Fi送信、簡単なQR・物体認識を担うエッジ端末として適している。任意の商品識別、前後画像の差分理解、賞味期限OCR、献立生成はクラウドまたはローカルPC上のVision-Language ModelとLLMへ分担する。

## ESP32の能力

Espressif公式のESP32-S3-EYEは次を備える。

- ESP32-S3R8
- AI向けベクトル命令
- 8 MB Octal SPI PSRAM
- 8 MB Flash
- OV2640 2MPカメラ
- Wi-Fi / Bluetooth LE
- MicroSD
- ESP-WHO / ESP-DLによる画像処理

ESP-WHOには顔認識、物体検出、QRコード認識などの例がある。したがって撮影、トリガー、軽量な前処理や認識は可能。ただし冷蔵庫内は遮蔽、類似パッケージ、残量、照明変化があるため、オンデバイスだけで汎用品を高精度に追跡するのは難しい。

ESP32-S3-EYEは完成度が高い一方、多くのGPIOがカメラ、LCD、SD等に使用済みで、複数の重量センサーを同じ基板へ増設する用途には向きにくい。ハッカソンでは次のどちらかが現実的。

1. ESP32-S3-EYEをカメラ端末にして、重量センサーは別ESP32へ接続する
2. PSRAM付きESP32-S3 DevKitとOV2640等のカメラを組み合わせ、必要なGPIOを確保する

## 在庫取得方式の比較

| 方式 | 得意 | 苦手 | MVP評価 |
|---|---|---|---|
| 冷蔵庫内カメラの定期撮影 | 配線が少なく、デモが分かりやすい | 奥の食品、重なり、残量、ドアポケット、照明変化 | 最短で作れるが精度は限定的 |
| ドア開閉前後の画像差分 | 入出庫イベントを追いやすい | 手や複数商品の同時移動、死角 | ハッカソン向き |
| ドア付近の通過カメラ | 商品を一つずつ大きく撮影できる | 動線と筐体設計が必要 | 精度と独自性のバランスが良い |
| バーコード・QR | 包装食品のIDが正確 | 生鮮品、バーコードをカメラへ向ける手間 | 補助手段として強い |
| ロードセル＋HX711 | 重量変化と残量を測れる | 何の商品かは分からない。棚ごとの配線が必要 | 一つの専用トレーなら有効 |
| レシート・購入履歴 | 入庫候補をまとめて取得できる | 消費・廃棄を検知できない | カメラとの併用向き |
| 手入力 | 確実 | 継続されにくい | 誤認識確認だけに限定する |

## 推奨するセンサー構成

### ハッカソンMVP

- ESP32-S3 + カメラ
- 磁気リードスイッチまたはHallセンサーでドア開閉を検知
- LEDで撮影時の照明を固定
- 冷蔵庫を閉じた直後に画像を撮影
- Wi-Fiでバックエンドへ画像とイベントを送信
- 任意で一つだけ、ロードセル＋HX711の「スマートトレー」を追加

### 実製品を意識した構成

冷蔵庫の奥を常時撮るより、食品が出入りするドア付近を「ゲート」にする。入出庫時に商品を大きく撮り、包装食品はバーコード、生鮮品は画像認識、残量が重要な食品は重量トレーで補完する。

## HX711と重量センサー

HX711はロードセルの微小信号をマイコンで扱える値へ変換するADC・アンプで、ClockとDataの2線で接続できる。SparkFunの資料では2.7〜5V動作、10または80 samples/sec。ESP32から扱いやすい。

重量だけでは商品種類を識別できないため、「牛乳置き場」「卵トレー」など対象を限定したスマートトレーとして使う。カメラ認識結果と組み合わせると、商品IDと残量を分担できる。

## 物理環境上のリスク

- 食品同士の遮蔽と、棚・ドアポケットの死角
- 庫内照明の位置と露出変化
- 低温・高湿度と、ドア開閉時の結露
- 金属筐体によるWi-Fi電波の減衰
- 電源線をドアパッキンへ通す難しさ
- 家族が複数商品を同時に出し入れする場合の追跡
- 容器だけ残る、別容器へ移す、少量だけ使う場合の残量推定

このため、基板を庫内に常設するより、可能ならドア外側または開口部付近に置く。庫内へ置く場合は防湿ケース、乾燥剤、ケーブル処理、電波試験が必要。

## 既存製品

Samsung Family Hub / AI Vision Insideは、庫内認識、食品管理、週間献立、レシピ、買い物リストまで提供しており、機能一覧だけでは今回の案と直接競合する。

Samsung公式サポートではAI Vision Inside 2.0が37種類の果物・野菜と一部の包装食品を自動分類し、追加で最大50種類の包装食品を端末上で学習できるとしている。大手の専用冷蔵庫でも対象が限定されることから、汎用品認識の難しさが分かる。

### 競合の分類

#### 冷蔵庫一体型・メーカー純正

| 競合 | 主な機能 | 今回の案との重なり |
|---|---|---|
| Samsung Family Hub / AI Vision Inside | 食材自動認識、在庫管理、献立、レシピ、買い物リスト、補充提案 | ほぼ全面的に重なる |
| LG SIGNATURE / ThinQ Food | 内蔵AIカメラによる食品認識、在庫・期限管理、在庫と好みに基づくレシピ提案 | ほぼ全面的に重なる |
| Panasonic 冷蔵庫AIカメラ | 開閉時の庫内撮影、野菜認識、利用期限目安、使い切りレシピ提案 | 後付けカメラと期限優先献立が重なる |
| Panasonic 重量検知プレート | 冷蔵庫内外の食品重量を測り、残量をアプリ表示 | スマートトレー案と重なる |

Panasonicのカメラは別売品だが、公式の対応冷蔵庫に限定される。2026年モデルを含む多数のPanasonic機種に対応している。野菜認識は特定60種・野菜室に限定され、それ以外はユーザー登録が必要と説明されている。

#### 後付けカメラ

| 競合 | 主な機能・状態 | 今回の案との重なり |
|---|---|---|
| Smarter FridgeCam | 冷蔵庫へ後付けするWi-Fiカメラ。最新庫内画像、食品認識、賞味期限管理。公式サイトで£99掲載 | 「どの冷蔵庫もスマート化」と直接競合 |
| In Already | 後付け冷蔵庫・パントリーカメラ。食品認識、リアルタイム在庫、期限通知、AIレシピ、家族共有 | 直接競合 |
| FreshCam by RetroLabs | 既存冷蔵庫へ短時間で設置し、在庫認識、期限通知、レシピ、再注文を目指す。現在のサイトはベータ参加募集 | コンセプトが直接競合 |
| HeySalad AI Camera | 飲食事業者向け。ESP32-S3と2MPカメラで、ドア開閉時に棚を読み在庫数へ変換。パイロット募集 | B2B用途で技術構成が近い |

#### アプリのみ

Fridge AI、PantryAI、Grocery AI、FreshDate、CozZo、FridgeApp.aiなどが、写真・レシートのスキャン、在庫、期限通知、献立、買い物リストを組み合わせている。したがって「写真を撮るとレシピを出す」だけでは差別化できない。

#### 個人開発・研究

`projectsmartfridge.com` では7台のESP32-CAMから画像を送信し、Claude Visionで食品を認識して在庫、買い物リスト、レシピ候補を作る実装が公開されている。ESP32-CAM＋クラウドVisionという技術構成自体も新規ではない。

### 競合調査からの判断

次の主張だけでは新規性にならない。

- 既存冷蔵庫への後付け
- カメラによる食品認識
- 在庫と賞味期限の管理
- 在庫からのレシピ提案
- 買い物リスト生成
- ESP32-CAMとクラウドAIの組み合わせ

差別化するなら、単なる機能の足し算ではなく、次のいずれかへ焦点を置く。

1. **不確実性を扱う在庫台帳**: カメラ、重量、レシート、調理履歴を照合し、確信度が低い差分だけ人に確認する。
2. **食品廃棄を減らす実行エージェント**: 期限、予定、調理時間から献立を決め、買い物リストまたは注文へ反映し、実際の消費まで追跡する。
3. **生活予定と連携する献立運用**: 家族の在宅人数、帰宅時刻、調理可能時間、外食予定を読み、現実に実行できる献立へ毎日再計画する。
4. **共有冷蔵庫向け**: 家族、シェアハウス、研究室などで所有者、共同購入、残量、当番まで調整する。
5. **B2Bの限定在庫**: 飲食店、研究室、保育施設など、対象品目を限定して棚卸しと発注判断を高精度化する。

### 差別化候補

- 高価なスマート冷蔵庫を買わず、既存の冷蔵庫へ後付けできる
- 一つのセンサーに依存せず、カメラ、重量、バーコード、レシートを統合する
- 認識できない食品だけ、写真付きの一タップ確認を求める
- 在庫表示で終わらず、期限と家族予定から献立を決め、買い物リストへ反映する
- 廃棄や外食も学習し、実際に食べられる献立へ適応する

## 参照先

- Espressif Edge AI workshop / ESP32-S3-EYE: https://developer.espressif.com/workshops/edge-ai-with-esp32-s3/introduction/
- ESP-WHO getting started: https://developer.espressif.com/blog/2026/05/esp-who-get-started/
- ESP32-S3-EYE guide: https://docs.espressif.com/projects/esp-board-manager/en/latest/references/boards/esp32_s3_eye.html
- ESP32-S3 product overview: https://docs.espressif.com/projects/esp-hardware-design-guidelines/en/latest/esp32s3/product-overview.html
- SparkFun HX711 guide: https://learn.sparkfun.com/tutorials/load-cell-amplifier-hx711-breakout-hookup-guide/hardware-hookup-
- Samsung Family Hub: https://www.samsung.com/us/explore/family-hub-refrigerator/overview/
- Samsung AI Vision Inside 2.0: https://www.samsung.com/uk/support/home-appliances/what-features-does-the-bespoke-ai-refrigerator-have/
- LG SIGNATURE / ThinQ Food: https://www.lg.com/us/press-release/HA-Press-Release-LG-SIGNATURE-2nd-Gen-CES-2025.pdf
- Panasonic 冷蔵庫AIカメラ: https://panasonic.jp/reizo/products/NY-PCZE2.html
- Panasonic 野菜使い切りサポート: https://panasonic.jp/reizo/app/kitchen-pocket/vegetable-ai.html
- Panasonic 重量検知プレート: https://panasonic.jp/reizo/option/NY-PZE1B1-NY-PZE1.html
- Smarter FridgeCam: https://smarter.am/products/smarter-fridgecam
- In Already: https://alreadyin.com/
- FreshCam by RetroLabs: https://retrolabs.io/
- HeySalad AI Camera: https://heysalad.ai/camera
- Project Smart Fridge: https://www.projectsmartfridge.com/
- Fridge AI: https://apps.apple.com/us/app/fridge-ai-food-recipes/id6739216407
- PantryAI: https://apps.apple.com/us/app/pantryai-recipe-keeper/id6747031054
- Grocery AI: https://www.groceryai.com/
