; graphmaster extraction query for Rust (tree-sitter-rust v0.24).
;
; Capture convention: see queries/go.scm. Rust specifics, completed by
; lang_rust.go's postProcess:
;   - impl blocks are captured as temporary @def.type nodes named after the
;     implementing type; postProcess merges them into the same-scope type
;     definition (methods become its children, `impl Trait for T` becomes an
;     implements edge of T) or, when the type lives elsewhere, turns their
;     items into top-level members scoped by the type name.
;   - use declarations are captured whole and expanded into one import per
;     leaf by postProcess; `mod x;` declarations become "self::x" imports.
;   - macro arguments are token trees, not expressions; calls inside them are
;     recognized heuristically (name followed by a parenthesized group).

; ---------------------------------------------------------------- imports

(use_declaration argument: (_) @import.path)

(mod_item !body) @import.path

; ---------------------------------------------------------------- definitions

; Functions: free (file, module or block scope), methods (impl and trait
; bodies) and foreign declarations.
(source_file
  (function_item name: (identifier) @name) @def.function)

(mod_item body: (declaration_list
  (function_item name: (identifier) @name) @def.function))

(block
  (function_item name: (identifier) @name) @def.function)

(impl_item body: (declaration_list
  (function_item name: (identifier) @name) @def.method))

(trait_item body: (declaration_list
  (function_item name: (identifier) @name) @def.method))

(trait_item body: (declaration_list
  (function_signature_item name: (identifier) @name) @def.method))

(foreign_mod_item body: (declaration_list
  (function_signature_item name: (identifier) @name) @def.function))

; Types.
(struct_item name: (type_identifier) @name) @def.struct

(union_item name: (type_identifier) @name) @def.struct

(enum_item name: (type_identifier) @name) @def.enum

(enum_variant name: (identifier) @name) @def.enum_member

(trait_item name: (type_identifier) @name) @def.trait

(source_file
  (type_item name: (type_identifier) @name) @def.type)

(mod_item body: (declaration_list
  (type_item name: (type_identifier) @name) @def.type))

(block
  (type_item name: (type_identifier) @name) @def.type)

(trait_item body: (declaration_list
  (associated_type name: (type_identifier) @name) @def.type))

(field_declaration_list
  (field_declaration name: (field_identifier) @name) @def.field)

; Constants and statics (associated constants in impl and trait bodies too).
(source_file
  (const_item name: (identifier) @name) @def.constant)

(mod_item body: (declaration_list
  (const_item name: (identifier) @name) @def.constant))

(impl_item body: (declaration_list
  (const_item name: (identifier) @name) @def.constant))

(trait_item body: (declaration_list
  (const_item name: (identifier) @name) @def.constant))

(source_file
  (static_item name: (identifier) @name) @def.variable)

(mod_item body: (declaration_list
  (static_item name: (identifier) @name) @def.variable))

; Inline modules (file modules are imports, see above).
(mod_item
  name: (identifier) @name
  body: (declaration_list)) @def.module

(macro_definition name: (identifier) @name) @def.macro

; Impl blocks (temporary, merged by postProcess). The name is the base name
; of the implementing type.
(impl_item
  type: [
    (type_identifier) @name
    (generic_type type: (type_identifier) @name)
    (scoped_type_identifier name: (type_identifier) @name)
    (generic_type type: (scoped_type_identifier name: (type_identifier) @name))
    (primitive_type) @name
    (reference_type type: [
      (type_identifier) @name
      (generic_type type: (type_identifier) @name)
      (primitive_type) @name
    ])
  ]) @def.type

; ---------------------------------------------------------------- tests

; #[test], #[tokio::test], #[rstest], ... possibly followed by up to two
; more attributes (e.g. #[ignore], #[should_panic]).
((attribute_item
  (attribute [
    (identifier) @_a
    (scoped_identifier name: (identifier) @_a)
  ]))
  .
  (function_item) @test
  (#any-of? @_a "test" "rstest" "test_case" "quickcheck"))

((attribute_item
  (attribute [
    (identifier) @_a
    (scoped_identifier name: (identifier) @_a)
  ]))
  .
  (attribute_item)
  .
  (function_item) @test
  (#any-of? @_a "test" "rstest" "test_case" "quickcheck"))

((attribute_item
  (attribute [
    (identifier) @_a
    (scoped_identifier name: (identifier) @_a)
  ]))
  .
  (attribute_item)
  .
  (attribute_item)
  .
  (function_item) @test
  (#any-of? @_a "test" "rstest" "test_case" "quickcheck"))

; #[cfg(test)] mod tests { ... }: flags the module (no test mark).
((attribute_item
  (attribute
    (identifier) @_cfg
    arguments: (token_tree . (identifier) @_t .)))
  .
  (mod_item body: (declaration_list)) @test
  (#eq? @_cfg "cfg")
  (#eq? @_t "test"))

; ---------------------------------------------------------------- locals

(type_parameters
  (type_parameter name: (type_identifier) @local))

; Associated types defined in impl blocks are not definitions; keep their
; names from becoming type references.
(impl_item body: (declaration_list
  (type_item name: (type_identifier) @local)))

; ---------------------------------------------------------------- references

; Calls.
(call_expression
  function: (identifier) @name) @ref.call

(call_expression
  function: (scoped_identifier
    path: (_) @qualifier
    name: (identifier) @name)) @ref.call

(call_expression
  function: (field_expression
    value: (_) @receiver
    field: (field_identifier) @name)) @ref.call

(call_expression
  function: (generic_function
    function: (identifier) @name)) @ref.call

(call_expression
  function: (generic_function
    function: (scoped_identifier
      path: (_) @qualifier
      name: (identifier) @name))) @ref.call

(call_expression
  function: (generic_function
    function: (field_expression
      value: (_) @receiver
      field: (field_identifier) @name))) @ref.call

(macro_invocation
  macro: (identifier) @name) @ref.call

(macro_invocation
  macro: (scoped_identifier
    path: (_) @qualifier
    name: (identifier) @name)) @ref.call

; Field access.
(field_expression
  value: (_) @receiver
  field: (field_identifier) @name) @ref.member

; Path values (enum variants, associated constants, functions passed as
; values) in common expression positions. Inner segments of longer paths
; are deliberately not matched.
(_ value: (scoped_identifier
  path: (_) @qualifier
  name: (identifier) @name) @ref.member)

(_ left: (scoped_identifier
  path: (_) @qualifier
  name: (identifier) @name) @ref.member)

(_ right: (scoped_identifier
  path: (_) @qualifier
  name: (identifier) @name) @ref.member)

(_ condition: (scoped_identifier
  path: (_) @qualifier
  name: (identifier) @name) @ref.member)

(_ body: (scoped_identifier
  path: (_) @qualifier
  name: (identifier) @name) @ref.member)

(arguments (scoped_identifier
  path: (_) @qualifier
  name: (identifier) @name) @ref.member)

(array_expression (scoped_identifier
  path: (_) @qualifier
  name: (identifier) @name) @ref.member)

(tuple_expression (scoped_identifier
  path: (_) @qualifier
  name: (identifier) @name) @ref.member)

(return_expression (scoped_identifier
  path: (_) @qualifier
  name: (identifier) @name) @ref.member)

(unary_expression (scoped_identifier
  path: (_) @qualifier
  name: (identifier) @name) @ref.member)

(match_pattern (scoped_identifier
  path: (_) @qualifier
  name: (identifier) @name) @ref.member)

(expression_statement (scoped_identifier
  path: (_) @qualifier
  name: (identifier) @name) @ref.member)

(block (scoped_identifier
  path: (_) @qualifier
  name: (identifier) @name) @ref.member)

(parenthesized_expression (scoped_identifier
  path: (_) @qualifier
  name: (identifier) @name) @ref.member)

(index_expression (scoped_identifier
  path: (_) @qualifier
  name: (identifier) @name) @ref.member)

(range_expression (scoped_identifier
  path: (_) @qualifier
  name: (identifier) @name) @ref.member)

(tuple_struct_pattern
  type: (scoped_identifier
    path: (_) @qualifier
    name: (identifier) @name) @ref.member)

; Types.
(scoped_type_identifier
  path: (_) @qualifier
  name: (type_identifier) @name) @ref.type

(type_identifier) @name @ref.type

; Calls and field accesses inside macro arguments (token trees nested up to
; four levels deep). A call is a name directly followed by a parenthesized
; group; "a::f(..)" is qualified, "x.f(..)" has a receiver.
(macro_invocation (token_tree
  ["(" "[" "{" "," ";" "=" "=>" "&" "*" "!" "-" "+" "/" "%" "|" "||" "&&"
   "==" "!=" "<" ">" "<=" ">=" ":" "?" ".." "return"]
  .
  (identifier) @name
  .
  (token_tree) @_args
  (#match? @_args "^[(]")) @ref.call)

(macro_invocation (token_tree (token_tree
  ["(" "[" "{" "," ";" "=" "=>" "&" "*" "!" "-" "+" "/" "%" "|" "||" "&&"
   "==" "!=" "<" ">" "<=" ">=" ":" "?" ".." "return"]
  .
  (identifier) @name
  .
  (token_tree) @_args
  (#match? @_args "^[(]")) @ref.call))

(macro_invocation (token_tree (token_tree (token_tree
  ["(" "[" "{" "," ";" "=" "=>" "&" "*" "!" "-" "+" "/" "%" "|" "||" "&&"
   "==" "!=" "<" ">" "<=" ">=" ":" "?" ".." "return"]
  .
  (identifier) @name
  .
  (token_tree) @_args
  (#match? @_args "^[(]")) @ref.call)))

(macro_invocation (token_tree (token_tree (token_tree (token_tree
  ["(" "[" "{" "," ";" "=" "=>" "&" "*" "!" "-" "+" "/" "%" "|" "||" "&&"
   "==" "!=" "<" ">" "<=" ">=" ":" "?" ".." "return"]
  .
  (identifier) @name
  .
  (token_tree) @_args
  (#match? @_args "^[(]")) @ref.call))))

(macro_invocation (token_tree
  [(identifier) (self) (super) (crate)] @qualifier
  .
  "::"
  .
  (identifier) @name
  .
  (token_tree) @_args
  (#match? @_args "^[(]")) @ref.call)

(macro_invocation (token_tree (token_tree
  [(identifier) (self) (super) (crate)] @qualifier
  .
  "::"
  .
  (identifier) @name
  .
  (token_tree) @_args
  (#match? @_args "^[(]")) @ref.call))

(macro_invocation (token_tree (token_tree (token_tree
  [(identifier) (self) (super) (crate)] @qualifier
  .
  "::"
  .
  (identifier) @name
  .
  (token_tree) @_args
  (#match? @_args "^[(]")) @ref.call)))

(macro_invocation (token_tree (token_tree (token_tree (token_tree
  [(identifier) (self) (super) (crate)] @qualifier
  .
  "::"
  .
  (identifier) @name
  .
  (token_tree) @_args
  (#match? @_args "^[(]")) @ref.call))))

(macro_invocation (token_tree
  [(identifier) (self)] @receiver
  .
  "."
  .
  (identifier) @name
  .
  (token_tree) @_args
  (#match? @_args "^[(]")) @ref.call)

(macro_invocation (token_tree (token_tree
  [(identifier) (self)] @receiver
  .
  "."
  .
  (identifier) @name
  .
  (token_tree) @_args
  (#match? @_args "^[(]")) @ref.call))

(macro_invocation (token_tree (token_tree (token_tree
  [(identifier) (self)] @receiver
  .
  "."
  .
  (identifier) @name
  .
  (token_tree) @_args
  (#match? @_args "^[(]")) @ref.call)))

(macro_invocation (token_tree
  [(identifier) (self)] @receiver
  .
  "."
  .
  (identifier) @name) @ref.member)

(macro_invocation (token_tree (token_tree
  [(identifier) (self)] @receiver
  .
  "."
  .
  (identifier) @name) @ref.member))

; ---------------------------------------------------------------- inheritance

; impl Trait for Type: the trait, attached to the temporary impl def.
(impl_item
  trait: [
    (type_identifier) (scoped_type_identifier) (generic_type)
  ] @inherit.implements)

; Supertraits: trait A: B + c::D.
(trait_item
  bounds: (trait_bounds [
    (type_identifier) (scoped_type_identifier) (generic_type)
  ] @inherit.extends))
