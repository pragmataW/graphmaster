; graphmaster extraction query for Go (tree-sitter-go v0.25).
;
; Capture convention (see internal/parse/engine.go):
;   @def.<kind> + @name [+ @scope]     definition; <kind> is a model.NodeKind name
;   @ref.call|@ref.type|@ref.member    reference; @name is the referenced
;     + @name [+ @receiver|@qualifier] identifier
;   @import.path [+ @import.alias|@import.name]
;   @inherit.extends|implements|embeds target type of the enclosing type def
;   @test                              marks the def whose node is captured
;   @package                           package clause name
;   @local                             name scoped to the enclosing def (type
;                                      params); shadows unqualified type refs
; Captures starting with "_" are predicate helpers and ignored.

; ---------------------------------------------------------------- package

(package_clause (package_identifier) @package)

; ---------------------------------------------------------------- imports

(import_spec
  name: (_) @import.alias
  path: (_) @import.path)

(import_spec
  !name
  path: (_) @import.path)

; ---------------------------------------------------------------- definitions

(function_declaration
  name: (identifier) @name) @def.function

; Methods: the receiver base type becomes the scope (pointer and type
; arguments stripped by the engine's type-name splitter).
(method_declaration
  receiver: (parameter_list
    (parameter_declaration
      type: [
        (type_identifier) @scope
        (pointer_type (type_identifier) @scope)
        (generic_type type: (type_identifier) @scope)
        (pointer_type (generic_type type: (type_identifier) @scope))
      ]))
  name: (field_identifier) @name) @def.method

(type_spec
  name: (type_identifier) @name
  type: (struct_type)) @def.struct

(type_spec
  name: (type_identifier) @name
  type: (interface_type)) @def.interface

(type_spec
  name: (type_identifier) @name
  type: [
    (type_identifier) (qualified_type) (generic_type) (pointer_type)
    (slice_type) (array_type) (map_type) (channel_type) (function_type)
    (negated_type) (parenthesized_type)
  ]) @def.type

(type_alias
  name: (type_identifier) @name) @def.type

(field_declaration
  name: (field_identifier) @name) @def.field

(method_elem
  name: (field_identifier) @name) @def.method

; Only package-level constants and variables are definitions.
(source_file
  (const_declaration
    (const_spec name: (identifier) @name) @def.constant))

(source_file
  (var_declaration
    (var_spec name: (identifier) @name) @def.variable))

(source_file
  (var_declaration
    (var_spec_list
      (var_spec name: (identifier) @name) @def.variable)))

; ---------------------------------------------------------------- tests

(function_declaration
  name: (identifier) @_name
  parameters: (parameter_list
    .
    (parameter_declaration
      type: (pointer_type
        (qualified_type
          package: (package_identifier) @_pkg
          name: (type_identifier) @_typ)))
    .)
  (#match? @_name "^(Test|Benchmark|Fuzz)([^a-z]|$)")
  (#eq? @_pkg "testing")
  (#any-of? @_typ "T" "B" "F")) @test

; ---------------------------------------------------------------- locals

(type_parameter_declaration
  name: (identifier) @local)

(method_declaration
  receiver: (parameter_list
    (parameter_declaration
      type: [
        (generic_type type_arguments: (type_arguments (type_elem (type_identifier) @local)))
        (pointer_type (generic_type type_arguments: (type_arguments (type_elem (type_identifier) @local))))
      ])))

; ---------------------------------------------------------------- references

(call_expression
  function: (identifier) @name) @ref.call

(call_expression
  function: (selector_expression
    operand: (_) @receiver
    field: (field_identifier) @name)) @ref.call

(call_expression
  function: (parenthesized_expression (identifier) @name)) @ref.call

(selector_expression
  operand: (_) @receiver
  field: (field_identifier) @name) @ref.member

(qualified_type
  package: (package_identifier) @qualifier
  name: (type_identifier) @name) @ref.type

(type_identifier) @name @ref.type

; ---------------------------------------------------------------- inheritance

; Struct embedding: an anonymous field.
(field_declaration
  !name
  type: (_) @inherit.embeds)

; Interface embedding: a type element that is a single named type
; (unions and ~approximations are constraints, not embedding).
(interface_type
  (type_elem
    .
    [(type_identifier) (qualified_type) (generic_type)] @inherit.embeds
    .))
