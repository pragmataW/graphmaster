; graphmaster extraction query for C (tree-sitter-c v0.24).
;
; Capture convention: see queries/go.scm. C specifics:
;   - every declaration is captured wherever it appears; definitions nested
;     in function bodies (locals) are dropped by the spec's postProcess, which
;     keeps this query free of parent-context patterns;
;   - prototypes and extern variables become definitions flagged
;     "declaration" by the spec (it inspects the captured node);
;   - #include keeps its delimiters ("x.h" / <x.h>) in Import.Spec.

; ---------------------------------------------------------------- includes

(preproc_include path: (_) @import.path)

; ---------------------------------------------------------------- macros

(preproc_def name: (identifier) @name value: (_)) @def.macro

; Valueless macros are definitions too, except include guards.
(preproc_def
  name: (identifier) @name
  !value
  (#not-match? @name "(_H|_HH|_HPP|_HXX|_INC|_INCLUDED|_DEFINED)_*$")) @def.macro

(preproc_function_def name: (identifier) @name) @def.macro

; ---------------------------------------------------------------- functions

; Definitions, with up to two pointer levels on the return type.
(function_definition
  declarator: [
    (function_declarator declarator: (identifier) @name)
    (pointer_declarator declarator: (function_declarator declarator: (identifier) @name))
    (pointer_declarator declarator: (pointer_declarator
      declarator: (function_declarator declarator: (identifier) @name)))
  ]) @def.function

; Prototypes (flagged as declarations by the spec).
(declaration
  declarator: [
    (function_declarator declarator: (identifier) @name)
    (pointer_declarator declarator: (function_declarator declarator: (identifier) @name))
    (pointer_declarator declarator: (pointer_declarator
      declarator: (function_declarator declarator: (identifier) @name)))
  ]) @def.function

; ---------------------------------------------------------------- variables

(declaration
  declarator: [
    (identifier) @name
    (pointer_declarator declarator: (identifier) @name)
    (pointer_declarator declarator: (pointer_declarator declarator: (identifier) @name))
    (array_declarator declarator: (identifier) @name)
    (pointer_declarator declarator: (array_declarator declarator: (identifier) @name))
    (init_declarator declarator: [
      (identifier) @name
      (pointer_declarator declarator: (identifier) @name)
      (pointer_declarator declarator: (pointer_declarator declarator: (identifier) @name))
      (array_declarator declarator: (identifier) @name)
      (pointer_declarator declarator: (array_declarator declarator: (identifier) @name))
    ])
  ]) @def.variable

; ---------------------------------------------------------------- types

(struct_specifier name: (type_identifier) @name body: (_)) @def.struct
(union_specifier name: (type_identifier) @name body: (_)) @def.struct
(enum_specifier name: (type_identifier) @name body: (_)) @def.enum

(enumerator name: (identifier) @name) @def.enum_member

(field_declaration
  declarator: [
    (field_identifier) @name
    (pointer_declarator declarator: (field_identifier) @name)
    (pointer_declarator declarator: (pointer_declarator declarator: (field_identifier) @name))
    (array_declarator declarator: (field_identifier) @name)
    (pointer_declarator declarator: (array_declarator declarator: (field_identifier) @name))
    (function_declarator declarator: (parenthesized_declarator
      (pointer_declarator declarator: (field_identifier) @name)))
  ]) @def.field

; typedef of an anonymous struct/union/enum: the typedef names the type.
(type_definition
  type: [(struct_specifier !name body: (_)) (union_specifier !name body: (_))]
  declarator: (type_identifier) @name) @def.struct

(type_definition
  type: (enum_specifier !name body: (_))
  declarator: (type_identifier) @name) @def.enum

; typedef of a named struct with a body: the struct is its own definition,
; so the alias covers only its name (keeps the struct out of its span).
(type_definition
  type: [
    (struct_specifier name: (_) body: (_))
    (union_specifier name: (_) body: (_))
    (enum_specifier name: (_) body: (_))
  ]
  declarator: (type_identifier) @name @def.type)

; Every other typedef.
(type_definition
  type: [
    (primitive_type) (type_identifier) (sized_type_specifier) (macro_type_specifier)
    (struct_specifier !body) (union_specifier !body) (enum_specifier !body)
  ]
  declarator: [
    (type_identifier) @name
    (pointer_declarator declarator: (type_identifier) @name)
    (pointer_declarator declarator: (pointer_declarator declarator: (type_identifier) @name))
    (array_declarator declarator: (type_identifier) @name)
    (function_declarator declarator: (type_identifier) @name)
    (function_declarator declarator: (parenthesized_declarator
      (pointer_declarator declarator: (type_identifier) @name)))
  ]) @def.type

; ---------------------------------------------------------------- tests

; Unity / CUnit / cmocka style: void test_xxx(void) or void test_xxx(void **state).
(function_definition
  type: (primitive_type) @_ret
  declarator: (function_declarator
    declarator: (identifier) @_name
    parameters: (parameter_list) @_params)
  (#eq? @_ret "void")
  (#match? @_name "^test(_|[A-Z])")
  (#match? @_params "^\\(\\s*(void\\s*(\\*\\s*\\*\\s*[A-Za-z_][A-Za-z0-9_]*)?)?\\s*\\)$")) @test

; ---------------------------------------------------------------- references

(call_expression function: (identifier) @name) @ref.call

(call_expression
  function: (field_expression
    argument: (_) @receiver
    field: (field_identifier) @name)) @ref.call

(call_expression
  function: (parenthesized_expression (identifier) @name)) @ref.call

(field_expression
  argument: (_) @receiver
  field: (field_identifier) @name) @ref.member

(type_identifier) @name @ref.type

; Uses of ALL_CAPS names (object-like macros, enum constants): best effort.
((identifier) @name @ref.member
  (#match? @name "^[A-Z][A-Z0-9_]*[A-Z0-9]$")
  (#not-any-of? @name "NULL" "EOF" "TRUE" "FALSE" "EXIT_SUCCESS" "EXIT_FAILURE"))
