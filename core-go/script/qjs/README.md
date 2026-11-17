# quickjs-ng 0.16.2

The C sources of [quickjs-ng](https://github.com/quickjs-ng/quickjs) 0.16.2 (`QJS_VERSION_*` in `quickjs.h`), copied
byte for byte from the `quickjs/` folder of the `rquickjs-sys` 0.14.0 crate: the engine the Rust core runs through
rquickjs 0.14, so script errors, stack traces and limits match it. `LICENSE` is quickjs-ng's (MIT).

Vendored: `quickjs.c`, `libregexp.c`, `libunicode.c`, `dtoa.c` and the headers rquickjs-sys's `build.rs` copies with
them (`builtin-*.h`, `cutils.h`, `dtoa.h`, `libregexp*.h`, `libunicode*.h`, `list.h`, `quickjs-atom.h`,
`quickjs-opcode.h`, `quickjs-c-atomics.h`, `quickjs.h`). They are built as that `build.rs` builds them for a GNU
target: `_GNU_SOURCE`, no `NDEBUG`, and on Windows `-DWIN32_LEAN_AND_MEAN -std=c11` (`qjs.go`'s cgo flags).

Never edit these files. Glue goes in `wrap.c`/`wrap.h` and `qjs.go`. To update, copy the same files from the
`rquickjs-sys` version the Rust core uses (`core/Cargo.lock`), check that the list in its `build.rs` is unchanged, and
update the version above. `.gitattributes` keeps git from changing their line endings.
