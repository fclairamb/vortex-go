# Changelog

## [0.2.0](https://github.com/fclairamb/vortex-go/compare/v0.1.3...v0.2.0) (2026-11-26)


### Features

* files carry user-defined metadata ([#71](https://github.com/fclairamb/vortex-go/issues/71)) ([cbdc2ac](https://github.com/fclairamb/vortex-go/commit/cbdc2ac3bf622b901e603f99ee5f847c213e1f35))


### Bug Fixes

* **deps:** update module github.com/fclairamb/vortex-go to v0.1.3 ([#69](https://github.com/fclairamb/vortex-go/issues/69)) ([fd2f884](https://github.com/fclairamb/vortex-go/commit/fd2f88435f4b255b4a75ac72f30ceb513c8dd290))
* **deps:** update rust crate vortex to v0.94.0 ([#74](https://github.com/fclairamb/vortex-go/issues/74)) ([a34de2d](https://github.com/fclairamb/vortex-go/commit/a34de2d6707f276f45bf2cd4a7d1d352da6345bc))
* **deps:** update rust crate vortex-alp to v0.94.0 ([#76](https://github.com/fclairamb/vortex-go/issues/76)) ([c4e3f5a](https://github.com/fclairamb/vortex-go/commit/c4e3f5ab5583e786c436b23f4c19cbe20c2f9cb7))
* revert the lone vortex 0.94 cargo bumps ([#74](https://github.com/fclairamb/vortex-go/issues/74), [#76](https://github.com/fclairamb/vortex-go/issues/76)), stop renovate from redoing them ([#75](https://github.com/fclairamb/vortex-go/issues/75)) ([6b7fceb](https://github.com/fclairamb/vortex-go/commit/6b7fceb181c9ca0d765aeee7eaad7be1b9768894))


### Performance Improvements

* **scan:** a filter scan reuses its small buffers, its nodes and its arrays ([#66](https://github.com/fclairamb/vortex-go/issues/66)) ([6adca93](https://github.com/fclairamb/vortex-go/commit/6adca933a71395a30b4783040c72050e19d5af46))
* **scan:** a scan gives the memory of its last batch back when the loop ends ([#72](https://github.com/fclairamb/vortex-go/issues/72)) ([edbf7b3](https://github.com/fclairamb/vortex-go/commit/edbf7b36dcf4d75cf252d0303983605a9c1e3098))
* **scan:** OnPair strings are decoded in one pass, and a range of rows reads its codes only ([#81](https://github.com/fclairamb/vortex-go/issues/81)) ([e2a18df](https://github.com/fclairamb/vortex-go/commit/e2a18df9cc7dc266cc20579460c7f0b9f233333f))
* **writer:** a dictionary of numbers compares words, and the statistics of integers without nulls take one comparison per row ([#82](https://github.com/fclairamb/vortex-go/issues/82)) ([0308f7e](https://github.com/fclairamb/vortex-go/commit/0308f7e66668685c307219c3763253fde08ac95d))
* **writer:** a small file is no bigger than its Parquet twin ([#70](https://github.com/fclairamb/vortex-go/issues/70)) ([9a97476](https://github.com/fclairamb/vortex-go/commit/9a9747603f6611cecce6ef3d2119974a313b4c44))

## [0.1.3](https://github.com/fclairamb/vortex-go/compare/v0.1.2...v0.1.3) (2026-11-22)


### Performance Improvements

* **writer:** faster, leaner writes: strings are no longer copied, decimals are held at their own width ([#60](https://github.com/fclairamb/vortex-go/issues/60)) ([2152598](https://github.com/fclairamb/vortex-go/commit/21525989b7d9b4f07561bb6a1253f1f6d37e4039)), and dictionary encoding looks its codes up in a dedicated table instead of a Go map ([#63](https://github.com/fclairamb/vortex-go/issues/63)) ([e070039](https://github.com/fclairamb/vortex-go/commit/e070039e56b3ad13592d7514fcb5a82d9096bb6c))
* **scan:** faster decoding: FastLanes rows no longer need a division, and the FoR, zigzag and ALP loops run without bounds checks ([#61](https://github.com/fclairamb/vortex-go/issues/61)) ([d776078](https://github.com/fclairamb/vortex-go/commit/d7760787221f31b943e6b888eb3afca6d533c5ed))


### Miscellaneous

* **deps:** the benchmark module now depends on vortex-go v0.1.2 ([#64](https://github.com/fclairamb/vortex-go/issues/64)) ([3ff7afb](https://github.com/fclairamb/vortex-go/commit/3ff7afb199c229a3224f7cfcbed0fe1fd08ee54d))

## [0.1.2](https://github.com/fclairamb/vortex-go/compare/v0.1.1...v0.1.2) (2026-11-22)


### Performance Improvements

* **writer:** smaller files under `Compact`: uuid columns are now compressed and single-zone zone-map tables are dropped, bringing the dbbat archive to parquet-go's size or below ([#57](https://github.com/fclairamb/vortex-go/issues/57)) ([26ce7b1](https://github.com/fclairamb/vortex-go/commit/26ce7b1c2e6caf5b5351ddc43e115cd4b0b51ac2))


### Miscellaneous

* **deps:** the benchmark module now depends on vortex-go v0.1.1 ([#59](https://github.com/fclairamb/vortex-go/issues/59)) ([debb723](https://github.com/fclairamb/vortex-go/commit/debb723226d640988fe7c8fbe0054d9917977a5a))

## [0.1.1](https://github.com/fclairamb/vortex-go/compare/v0.1.0...v0.1.1) (2026-11-22)


Faster scans and a faster writer, on the same format.


### Performance Improvements

* **Scan**: decoding does less work per row
  * a FoR over bit-packed integers adds its reference as the blocks are unpacked ([#50](https://github.com/fclairamb/vortex-go/issues/50)) ([bc6720a](https://github.com/fclairamb/vortex-go/commit/bc6720aa8c491de4f8e74f4e4dbfb088873d22d3))
  * FSST makes the ranges of its rows in one pass, checks 8 codes at once ([#47](https://github.com/fclairamb/vortex-go/issues/47)) ([f3b3534](https://github.com/fclairamb/vortex-go/commit/f3b3534d3816d710ec0c010a351fbfb0f2542b77)), and checks the offsets and lengths of its rows with no branch per row ([#56](https://github.com/fclairamb/vortex-go/issues/56)) ([bc792df](https://github.com/fclairamb/vortex-go/commit/bc792dff3014786c6aeb1063928e36ec6987a8af))
  * a node of an array allocates nothing for its children, and nodes are allocated 16 at a time ([#52](https://github.com/fclairamb/vortex-go/issues/52)) ([b7add87](https://github.com/fclairamb/vortex-go/commit/b7add877047299a1f2b8182b3a00773029b264dd))
* **Writer**: integers skip the encodings their reference rules out, and the trials only measure instead of encoding ([#54](https://github.com/fclairamb/vortex-go/issues/54)) ([554318c](https://github.com/fclairamb/vortex-go/commit/554318c928be18b929a7b4eac023cd2cd1a950d9))


### Bug Fixes

* **deps:** the benchmarks module uses vortex-go v0.1.0 ([#55](https://github.com/fclairamb/vortex-go/issues/55)) ([aefa112](https://github.com/fclairamb/vortex-go/commit/aefa112b486dd2567734af011edfaf491669132e))

## 0.1.0 (2026-11-22)

First release: a pure-Go reader and writer for the Vortex columnar format, benchmarked against the Rust reference.


### Features

* **Reader**: open, scan, filter and decode into `Rows[T]`, with every encoding of the edition ([#9](https://github.com/fclairamb/vortex-go/issues/9)) ([455c769](https://github.com/fclairamb/vortex-go/commit/455c7692f64b506ef9ebd3af158699b0e1ff6558)); `OpenMapped` reads a local file in place ([#31](https://github.com/fclairamb/vortex-go/issues/31)) ([7a85ff8](https://github.com/fclairamb/vortex-go/commit/7a85ff8b2581de6a9df51e370de55dcbcfea55cc))
* **Writer**: `NewWriter`, `RowWriter[T]` and every encoder, with a compressor within 10% of the reference ([#10](https://github.com/fclairamb/vortex-go/issues/10)) ([3c7da41](https://github.com/fclairamb/vortex-go/commit/3c7da41625d6deb2022a8d81b68e47dc13fe3b9b)); masked, list, varbin, bytebool, chunked and Variant arrays, older editions, OnPair and Pco encoders ([#15](https://github.com/fclairamb/vortex-go/issues/15)) ([d5cb350](https://github.com/fclairamb/vortex-go/commit/d5cb3501116faa1cebc5d787eb3bfb709683eb55))
* **Benchmarks**: history next to the Rust reference, a GitHub Pages site and README badges ([#12](https://github.com/fclairamb/vortex-go/issues/12)) ([eb4687b](https://github.com/fclairamb/vortex-go/commit/eb4687bba0a591d184f43e8d41083e96b8588adf)); the site compares with parquet-go, on its own page, every read measured with and without a mapped file ([#34](https://github.com/fclairamb/vortex-go/issues/34)) ([b2317c4](https://github.com/fclairamb/vortex-go/commit/b2317c4f56547094eaeaf3f91015415b7037e4d5)) ([#36](https://github.com/fclairamb/vortex-go/issues/36)) ([114b98a](https://github.com/fclairamb/vortex-go/commit/114b98a2d175b3c27d1b0f8311cdf10cea6e3dab)); one benchmark comment per PR, with interleaved runs and tables ([#18](https://github.com/fclairamb/vortex-go/issues/18)) ([068c961](https://github.com/fclairamb/vortex-go/commit/068c961091bd30af49fa60b5d103e0986913a629))


### Performance Improvements

* **Scan**: 3 to 10 times faster, on par with the Rust reader on the generated file ([#21](https://github.com/fclairamb/vortex-go/issues/21)) ([c4d49e6](https://github.com/fclairamb/vortex-go/commit/c4d49e66f08408f740cad1b362c6eaf06f234a2c))
  * a filter that keeps few rows, or a scan of part of a segment, decodes only those rows ([#23](https://github.com/fclairamb/vortex-go/issues/23)) ([93a380d](https://github.com/fclairamb/vortex-go/commit/93a380deb293837e44ca7898cc0580e756c970df)) ([#24](https://github.com/fclairamb/vortex-go/issues/24)) ([4d51323](https://github.com/fclairamb/vortex-go/commit/4d5132360b4a6354e8fe0125cc47928b4c54142c))
  * a filter on an integer column is one branchless comparison per row ([#49](https://github.com/fclairamb/vortex-go/issues/49)) ([b5864d5](https://github.com/fclairamb/vortex-go/commit/b5864d55808f02ae1ef70705ca7eab0ad43c58dd))
  * FSST in one pass, FastLanes 8 bytes at a time, dictionary validity with no per-row test ([#33](https://github.com/fclairamb/vortex-go/issues/33)) ([d140a98](https://github.com/fclairamb/vortex-go/commit/d140a98687d5a1a80d86e1aaceecc1f64c61b966)); a dictionary checks its codes once and builds a row in one lookup ([#46](https://github.com/fclairamb/vortex-go/issues/46)) ([56dac6f](https://github.com/fclairamb/vortex-go/commit/56dac6f9dcfc9b0f525c865971dedfe704be2b0f))
  * a scan reuses the memory of the previous one ([#32](https://github.com/fclairamb/vortex-go/issues/32)) ([b0d9e2b](https://github.com/fclairamb/vortex-go/commit/b0d9e2bd160a021915346e3c227ace746606f718)); a small file allocates only a quarter more than it decodes ([#48](https://github.com/fclairamb/vortex-go/issues/48)) ([b0a5d63](https://github.com/fclairamb/vortex-go/commit/b0a5d631a08b12827512355d937eaee787cdae00))
  * `Rows`: the strings of a field share one block per batch ([#42](https://github.com/fclairamb/vortex-go/issues/42)) ([805b2d7](https://github.com/fclairamb/vortex-go/commit/805b2d70b6ca7091ecdc04e9c2fcc5ca00c4213e))
* **Writer**: columns and chunks are compressed on every core, writes stay in order, and `WriteOptions.Buffer` bounds the memory ([#22](https://github.com/fclairamb/vortex-go/issues/22)) ([621568b](https://github.com/fclairamb/vortex-go/commit/621568b6e5c955a40d039fdcab7998f67950aae1)) ([#29](https://github.com/fclairamb/vortex-go/issues/29)) ([48b7e49](https://github.com/fclairamb/vortex-go/commit/48b7e499f54a5f64631c3950801be74d05bd3aa1)) ([#41](https://github.com/fclairamb/vortex-go/issues/41)) ([76457bb](https://github.com/fclairamb/vortex-go/commit/76457bbd141b00a585652af1bff16fb5f21f20fc))
  * Pco codes a chunk 2 to 4 times faster ([#38](https://github.com/fclairamb/vortex-go/issues/38)) ([5ac8183](https://github.com/fclairamb/vortex-go/commit/5ac8183927fcca100707a942e2338fb44becf8f5))
  * small files, numeric dictionaries and sized reserves ([#30](https://github.com/fclairamb/vortex-go/issues/30)) ([3c55217](https://github.com/fclairamb/vortex-go/commit/3c552179bd3cbe8209e12e5a1aadf5e49e632eed)); strings skip the dictionary the reference skips, and the probe stops early ([#43](https://github.com/fclairamb/vortex-go/issues/43)) ([e6d29f5](https://github.com/fclairamb/vortex-go/commit/e6d29f51f24fa2604593dfb3891a56cf898c084d))
  * encoders keep their memory and share their trainings ([#44](https://github.com/fclairamb/vortex-go/issues/44)) ([89b4ea7](https://github.com/fclairamb/vortex-go/commit/89b4ea77ae9b6ee9e059349b3f798d7219375e51)); zstd buffers are no longer zeroed before being written ([#37](https://github.com/fclairamb/vortex-go/issues/37)) ([4dd8729](https://github.com/fclairamb/vortex-go/commit/4dd87295b9ccf60cdcb65a094e1a818d8a705a61))


### Bug Fixes

* **bench:** a 1-thread run really uses 1 thread, and PRs are compared on TPC-H and ClickBench ([#45](https://github.com/fclairamb/vortex-go/issues/45)) ([a58ce9a](https://github.com/fclairamb/vortex-go/commit/a58ce9ab5c79a00f5d6c306d93beca209df481c7)); the `Rows` harness no longer allocates a row per row ([#40](https://github.com/fclairamb/vortex-go/issues/40)) ([9c9c15a](https://github.com/fclairamb/vortex-go/commit/9c9c15a1e95970ce7a53d68ec2c62201a4ee219c))
* **deps:** arrow-rs back to 59 in variantjson, the version vortex 0.93 uses ([#25](https://github.com/fclairamb/vortex-go/issues/25)) ([1f4b635](https://github.com/fclairamb/vortex-go/commit/1f4b6350524068f0cee7ef60ed5e35acd95f6b64)) (after a bump to v60 ([#20](https://github.com/fclairamb/vortex-go/issues/20)) ([27b29bb](https://github.com/fclairamb/vortex-go/commit/27b29bbadadb8d1d6333ad7ab17d8662c81bdbee))); klauspost/compress 1.20.5 ([#16](https://github.com/fclairamb/vortex-go/issues/16)) ([59ed085](https://github.com/fclairamb/vortex-go/commit/59ed085d529d7e316aa6aab8a7776f11482dd702)); renovate group names without "(major)" ([#7](https://github.com/fclairamb/vortex-go/issues/7)) ([8e187b2](https://github.com/fclairamb/vortex-go/commit/8e187b2a14904e89e3a696cc40718026c5030444))
