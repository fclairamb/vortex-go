# Changelog

## 0.1.0 (2026-11-22)


### Features

* benchmark history next to the Rust reference, a GitHub Pages site, README badges ([#12](https://github.com/fclairamb/vortex-go/issues/12)) ([eb4687b](https://github.com/fclairamb/vortex-go/commit/eb4687bba0a591d184f43e8d41083e96b8588adf))
* **ci:** one benchmark comment per PR, interleaved runs, tables ([#18](https://github.com/fclairamb/vortex-go/issues/18)) ([068c961](https://github.com/fclairamb/vortex-go/commit/068c961091bd30af49fa60b5d103e0986913a629))
* OpenMapped reads a local file where it is ([#31](https://github.com/fclairamb/vortex-go/issues/31)) ([7a85ff8](https://github.com/fclairamb/vortex-go/commit/7a85ff8b2581de6a9df51e370de55dcbcfea55cc))
* **site:** parquet-go has its own page, every read is measured with and without a mapped file ([#36](https://github.com/fclairamb/vortex-go/issues/36)) ([114b98a](https://github.com/fclairamb/vortex-go/commit/114b98a2d175b3c27d1b0f8311cdf10cea6e3dab))
* **site:** the page compares with parquet-go, and the numbers are recorded again ([#34](https://github.com/fclairamb/vortex-go/issues/34)) ([b2317c4](https://github.com/fclairamb/vortex-go/commit/b2317c4f56547094eaeaf3f91015415b7037e4d5))
* Vortex reader (open, scan, filter, Rows[T], every encoding of the edition) ([#9](https://github.com/fclairamb/vortex-go/issues/9)) ([455c769](https://github.com/fclairamb/vortex-go/commit/455c7692f64b506ef9ebd3af158699b0e1ff6558))
* Vortex writer (NewWriter, RowWriter[T], every encoder, compressor within 10% of the reference) ([#10](https://github.com/fclairamb/vortex-go/issues/10)) ([3c7da41](https://github.com/fclairamb/vortex-go/commit/3c7da41625d6deb2022a8d81b68e47dc13fe3b9b))
* writer covers masked, list, varbin, bytebool, chunked and Variant, older editions, OnPair and Pco encoders ([#15](https://github.com/fclairamb/vortex-go/issues/15)) ([d5cb350](https://github.com/fclairamb/vortex-go/commit/d5cb3501116faa1cebc5d787eb3bfb709683eb55))


### Bug Fixes

* **bench:** a run at 1 thread is run at 1 thread, and a PR is compared on TPC-H and ClickBench ([#45](https://github.com/fclairamb/vortex-go/issues/45)) ([a58ce9a](https://github.com/fclairamb/vortex-go/commit/a58ce9ab5c79a00f5d6c306d93beca209df481c7))
* **bench:** the Rows harness no longer allocates a row per row ([#40](https://github.com/fclairamb/vortex-go/issues/40)) ([9c9c15a](https://github.com/fclairamb/vortex-go/commit/9c9c15a1e95970ce7a53d68ec2c62201a4ee219c))
* **deps:** back to arrow-rs 59 in variantjson, the version vortex 0.93 uses ([#25](https://github.com/fclairamb/vortex-go/issues/25)) ([1f4b635](https://github.com/fclairamb/vortex-go/commit/1f4b6350524068f0cee7ef60ed5e35acd95f6b64))
* **deps:** update arrow-rs monorepo to v60 ([#20](https://github.com/fclairamb/vortex-go/issues/20)) ([27b29bb](https://github.com/fclairamb/vortex-go/commit/27b29bbadadb8d1d6333ad7ab17d8662c81bdbee))
* **deps:** update module github.com/klauspost/compress to v1.20.5 ([#16](https://github.com/fclairamb/vortex-go/issues/16)) ([59ed085](https://github.com/fclairamb/vortex-go/commit/59ed085d529d7e316aa6aab8a7776f11482dd702))
* **renovate:** drop "(major)" from the major group names ([#7](https://github.com/fclairamb/vortex-go/issues/7)) ([8e187b2](https://github.com/fclairamb/vortex-go/commit/8e187b2a14904e89e3a696cc40718026c5030444))


### Performance Improvements

* **rows:** the strings of a field share one block per batch ([#42](https://github.com/fclairamb/vortex-go/issues/42)) ([805b2d7](https://github.com/fclairamb/vortex-go/commit/805b2d70b6ca7091ecdc04e9c2fcc5ca00c4213e))
* **scan:** a dictionary checks its codes once, and makes a row in one lookup ([#46](https://github.com/fclairamb/vortex-go/issues/46)) ([56dac6f](https://github.com/fclairamb/vortex-go/commit/56dac6f9dcfc9b0f525c865971dedfe704be2b0f))
* **scan:** a file of few rows allocates a quarter more than it decodes, and runs fill their rows ([#48](https://github.com/fclairamb/vortex-go/issues/48)) ([b0a5d63](https://github.com/fclairamb/vortex-go/commit/b0a5d631a08b12827512355d937eaee787cdae00))
* **scan:** a filter on an integer column is one comparison per row, with no branch ([#49](https://github.com/fclairamb/vortex-go/issues/49)) ([b5864d5](https://github.com/fclairamb/vortex-go/commit/b5864d55808f02ae1ef70705ca7eab0ad43c58dd))
* **scan:** a filter that keeps few rows decodes these rows only ([#23](https://github.com/fclairamb/vortex-go/issues/23)) ([93a380d](https://github.com/fclairamb/vortex-go/commit/93a380deb293837e44ca7898cc0580e756c970df))
* **scan:** a scan of a part of a segment decodes its rows only ([#24](https://github.com/fclairamb/vortex-go/issues/24)) ([4d51323](https://github.com/fclairamb/vortex-go/commit/4d5132360b4a6354e8fe0125cc47928b4c54142c))
* **scan:** a scan takes the memory the scan before left ([#32](https://github.com/fclairamb/vortex-go/issues/32)) ([b0d9e2b](https://github.com/fclairamb/vortex-go/commit/b0d9e2bd160a021915346e3c227ace746606f718))
* **scan:** FSST in one pass, FastLanes 8 bytes at a time, dictionary validity with no test per row ([#33](https://github.com/fclairamb/vortex-go/issues/33)) ([d140a98](https://github.com/fclairamb/vortex-go/commit/d140a98687d5a1a80d86e1aaceecc1f64c61b966))
* **scan:** scans 3 to 10 times faster, on par with the Rust reader on the generated file ([#21](https://github.com/fclairamb/vortex-go/issues/21)) ([c4d49e6](https://github.com/fclairamb/vortex-go/commit/c4d49e66f08408f740cad1b362c6eaf06f234a2c))
* **writer:** Close finishes the columns on every core ([#41](https://github.com/fclairamb/vortex-go/issues/41)) ([76457bb](https://github.com/fclairamb/vortex-go/commit/76457bbd141b00a585652af1bff16fb5f21f20fc))
* **writer:** compress the chunks on every core, WriteOptions.Buffer to bound the memory ([#22](https://github.com/fclairamb/vortex-go/issues/22)) ([621568b](https://github.com/fclairamb/vortex-go/commit/621568b6e5c955a40d039fdcab7998f67950aae1))
* **writer:** Pco codes a chunk 2 to 4 times faster ([#38](https://github.com/fclairamb/vortex-go/issues/38)) ([5ac8183](https://github.com/fclairamb/vortex-go/commit/5ac8183927fcca100707a942e2338fb44becf8f5))
* **writer:** small files, dictionaries of numbers and sized reserves ([#30](https://github.com/fclairamb/vortex-go/issues/30)) ([3c55217](https://github.com/fclairamb/vortex-go/commit/3c552179bd3cbe8209e12e5a1aadf5e49e632eed))
* **writer:** strings are not tried as a dictionary the reference skips, and the probe stops when it knows ([#43](https://github.com/fclairamb/vortex-go/issues/43)) ([e6d29f5](https://github.com/fclairamb/vortex-go/commit/e6d29f51f24fa2604593dfb3891a56cf898c084d))
* **writer:** the buffers of zstd are not zeroed before they are written ([#37](https://github.com/fclairamb/vortex-go/issues/37)) ([4dd8729](https://github.com/fclairamb/vortex-go/commit/4dd87295b9ccf60cdcb65a094e1a818d8a705a61))
* **writer:** the columns of a Write work on every core, the writes stay in order ([#29](https://github.com/fclairamb/vortex-go/issues/29)) ([48b7e49](https://github.com/fclairamb/vortex-go/commit/48b7e499f54a5f64631c3950801be74d05bd3aa1))
* **writer:** the encoders keep their memory and share their trainings ([#44](https://github.com/fclairamb/vortex-go/issues/44)) ([89b4ea7](https://github.com/fclairamb/vortex-go/commit/89b4ea77ae9b6ee9e059349b3f798d7219375e51))
