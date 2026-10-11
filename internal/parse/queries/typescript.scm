; graphmaster extraction query for TypeScript (tree-sitter-typescript v0.23,
; "typescript" dialect). The TSX dialect uses this query plus tsx.scm.
;
; Capture convention: see queries/go.scm. JS-family specifics:
;   - import names are captured as raw text ("{ a, b as c }", an object
;     pattern, or a default import identifier) and normalized by the
;     language's postProcess hook (lang_javascript.go);
;   - when several patterns match the same node, the first pattern wins
;     (function-valued declarators before constants, constructors before
;     methods, imports with a clause before side-effect imports).

; ---------------------------------------------------------------- imports

(import_statement
  (import_clause
    (identifier)? @import.name
    (namespace_import (identifier) @import.alias)?
    (named_imports)? @import.name)
  source: (string (string_fragment) @import.path))

(import_statement
  (import_require_clause
    (identifier) @import.alias
    source: (string (string_fragment) @import.path)))

; Side-effect import: import "./polyfills";
(import_statement
  source: (string (string_fragment) @import.path))

; Re-exports.
(export_statement
  (export_clause) @import.name
  source: (string (string_fragment) @import.path))

(export_statement
  (namespace_export (identifier) @import.alias)
  source: (string (string_fragment) @import.path))

(export_statement
  "*" @import.wildcard
  source: (string (string_fragment) @import.path))

; CommonJS require.
(variable_declarator
  name: (identifier) @import.alias
  value: (call_expression
    function: (identifier) @_req
    arguments: (arguments . (string (string_fragment) @import.path) .))
  (#eq? @_req "require"))

(variable_declarator
  name: (object_pattern) @import.name
  value: (call_expression
    function: (identifier) @_req
    arguments: (arguments . (string (string_fragment) @import.path) .))
  (#eq? @_req "require"))

(call_expression
  function: (identifier) @_req
  arguments: (arguments . (string (string_fragment) @import.path) .)
  (#eq? @_req "require"))

; Dynamic import("./x").
(call_expression
  function: (import)
  arguments: (arguments . (string (string_fragment) @import.path)))

; ---------------------------------------------------------------- definitions

(function_declaration
  name: (identifier) @name) @def.function

(generator_function_declaration
  name: (identifier) @name) @def.function

; Ambient declarations and overload signatures; postProcess collapses a
; signature followed by its implementation into one definition.
(function_signature
  name: (identifier) @name) @def.function

(class_declaration
  name: (type_identifier) @name) @def.class

(abstract_class_declaration
  name: (type_identifier) @name) @def.class

(class
  name: (type_identifier) @name) @def.class

(interface_declaration
  name: (type_identifier) @name) @def.interface

(type_alias_declaration
  name: (type_identifier) @name) @def.type

(enum_declaration
  name: (identifier) @name) @def.enum

(enum_body
  (property_identifier) @name @def.enum_member)

(enum_body
  (enum_assignment
    name: (property_identifier) @name) @def.enum_member)

(internal_module
  name: (identifier) @name) @def.namespace

(module
  name: (identifier) @name) @def.namespace

(method_definition
  name: (property_identifier) @name
  (#eq? @name "constructor")) @def.constructor

(method_definition
  name: [(property_identifier) (private_property_identifier)] @name) @def.method

(abstract_method_signature
  name: [(property_identifier) (private_property_identifier)] @name) @def.method

; Members of interfaces, ambient classes and named object types; inline
; object types (parameters, type arguments) declare no symbols.
(interface_body
  (method_signature
    name: [(property_identifier) (private_property_identifier)] @name) @def.method)

(class_body
  (method_signature
    name: [(property_identifier) (private_property_identifier)] @name) @def.method)

(type_alias_declaration
  value: (object_type
    (method_signature
      name: [(property_identifier) (private_property_identifier)] @name) @def.method))

(interface_body
  (property_signature
    name: [(property_identifier) (private_property_identifier)] @name) @def.field)

(type_alias_declaration
  value: (object_type
    (property_signature
      name: [(property_identifier) (private_property_identifier)] @name) @def.field))

; Fields: function-valued ones are methods. Patterns that start and finish
; at the same nodes are reported in pattern order and the first one wins,
; hence "value: (_)" in the generic pattern.
(public_field_definition
  name: [(property_identifier) (private_property_identifier)] @name
  value: [(arrow_function) (function_expression) (generator_function)]) @def.method

(public_field_definition
  name: [(property_identifier) (private_property_identifier)] @name
  value: (_)) @def.field

(public_field_definition
  name: [(property_identifier) (private_property_identifier)] @name
  !value) @def.field

; Constructor parameter properties: constructor(public id: string). They
; nest inside the constructor by span; postProcess moves them to the class.
(method_definition
  name: (property_identifier) @_ctor
  parameters: (formal_parameters
    [
      (required_parameter (accessibility_modifier) pattern: (identifier) @name)
      (optional_parameter (accessibility_modifier) pattern: (identifier) @name)
    ] @def.field)
  (#eq? @_ctor "constructor"))

; Object literal members bound to a name: const api = { put: () => {} }
; (shorthand methods are method_definition nodes, matched above).
(variable_declarator
  value: (object
    (pair
      key: (property_identifier) @name
      value: [(arrow_function) (function_expression) (generator_function)]) @def.method))

; Function values bound to a name: module-level and local helpers.
; Module-level declarators start at "program" so that, for the same
; declarator, the function pattern precedes the constant/variable one.

(program
  (lexical_declaration
    (variable_declarator
      name: (identifier) @name
      value: [(arrow_function) (function_expression) (generator_function)]) @def.function))

(program
  (variable_declaration
    (variable_declarator
      name: (identifier) @name
      value: [(arrow_function) (function_expression) (generator_function)]) @def.function))

(program
  (export_statement
    declaration: (lexical_declaration
      (variable_declarator
        name: (identifier) @name
        value: [(arrow_function) (function_expression) (generator_function)]) @def.function)))

(program
  (export_statement
    declaration: (variable_declaration
      (variable_declarator
        name: (identifier) @name
        value: [(arrow_function) (function_expression) (generator_function)]) @def.function)))

(statement_block
  (lexical_declaration
    (variable_declarator
      name: (identifier) @name
      value: [(arrow_function) (function_expression) (generator_function)]) @def.function))

(statement_block
  (variable_declaration
    (variable_declarator
      name: (identifier) @name
      value: [(arrow_function) (function_expression) (generator_function)]) @def.function))

(statement_block
  (export_statement
    declaration: (lexical_declaration
      (variable_declarator
        name: (identifier) @name
        value: [(arrow_function) (function_expression) (generator_function)]) @def.function)))

(statement_block
  (export_statement
    declaration: (variable_declaration
      (variable_declarator
        name: (identifier) @name
        value: [(arrow_function) (function_expression) (generator_function)]) @def.function)))

; Module-level constants and variables.

(program
  (lexical_declaration
    kind: "const"
    (variable_declarator name: (identifier) @name value: (_)) @def.constant))

(program
  (lexical_declaration
    kind: "const"
    (variable_declarator name: (identifier) @name !value) @def.constant))

(program
  (lexical_declaration
    kind: "let"
    (variable_declarator name: (identifier) @name value: (_)) @def.variable))

(program
  (lexical_declaration
    kind: "let"
    (variable_declarator name: (identifier) @name !value) @def.variable))

(program
  (variable_declaration
    (variable_declarator name: (identifier) @name value: (_)) @def.variable))

(program
  (variable_declaration
    (variable_declarator name: (identifier) @name !value) @def.variable))

(program
  (export_statement
    declaration: (lexical_declaration
      kind: "const"
      (variable_declarator name: (identifier) @name value: (_)) @def.constant)))

(program
  (export_statement
    declaration: (lexical_declaration
      kind: "const"
      (variable_declarator name: (identifier) @name !value) @def.constant)))

(program
  (export_statement
    declaration: (lexical_declaration
      kind: "let"
      (variable_declarator name: (identifier) @name value: (_)) @def.variable)))

(program
  (export_statement
    declaration: (lexical_declaration
      kind: "let"
      (variable_declarator name: (identifier) @name !value) @def.variable)))

(program
  (export_statement
    declaration: (variable_declaration
      (variable_declarator name: (identifier) @name value: (_)) @def.variable)))

(program
  (export_statement
    declaration: (variable_declaration
      (variable_declarator name: (identifier) @name !value) @def.variable)))

; CommonJS exports: exports.f = function () {}, module.exports.g = () => {}.
(assignment_expression
  left: (member_expression
    object: (identifier) @_exports
    property: (property_identifier) @name)
  right: [(arrow_function) (function_expression) (generator_function)]
  (#eq? @_exports "exports")) @def.function

(assignment_expression
  left: (member_expression
    object: (member_expression
      object: (identifier) @_module
      property: (property_identifier) @_exports)
    property: (property_identifier) @name)
  right: [(arrow_function) (function_expression) (generator_function)]
  (#eq? @_module "module")
  (#eq? @_exports "exports")) @def.function

; Prototype methods: Foo.prototype.bar = function () {}.
(assignment_expression
  left: (member_expression
    object: (member_expression
      object: (identifier) @scope
      property: (property_identifier) @_proto)
    property: (property_identifier) @name)
  right: [(arrow_function) (function_expression) (generator_function)]
  (#eq? @_proto "prototype")) @def.method

; ---------------------------------------------------------------- tests

; describe("name", () => {}), it.only("name", function () {}), test(...).
(call_expression
  function: [
    (identifier) @_fn
    (member_expression object: (identifier) @_fn)
  ]
  arguments: (arguments
    .
    (string (string_fragment) @name)
    [(arrow_function) (function_expression)])
  (#any-of? @_fn "describe" "context" "suite" "it" "test" "specify")) @def.function @test

; it.each([...])("name %d", (n) => {})
(call_expression
  function: (call_expression
    function: (member_expression
      object: (identifier) @_fn
      property: (property_identifier) @_each))
  arguments: (arguments
    .
    (string (string_fragment) @name)
    [(arrow_function) (function_expression)])
  (#any-of? @_fn "describe" "it" "test")
  (#eq? @_each "each")) @def.function @test

; ---------------------------------------------------------------- locals

(type_parameter
  name: (type_identifier) @local)

; ---------------------------------------------------------------- references

(call_expression
  function: (identifier) @name) @ref.call

(call_expression
  function: (member_expression
    object: (_) @receiver
    property: [(property_identifier) (private_property_identifier)] @name)) @ref.call

(new_expression
  constructor: (identifier) @name) @ref.call

(new_expression
  constructor: (member_expression
    object: (_) @receiver
    property: (property_identifier) @name)) @ref.call

(decorator
  (identifier) @name) @ref.call

(member_expression
  object: (_) @receiver
  property: [(property_identifier) (private_property_identifier)] @name) @ref.member

(nested_type_identifier
  module: (_) @qualifier
  name: (type_identifier) @name) @ref.type

(type_identifier) @name @ref.type

; ---------------------------------------------------------------- inheritance

(class_heritage
  (extends_clause
    value: [(identifier) (member_expression)] @inherit.extends))

(class_heritage
  (implements_clause
    [(type_identifier) (generic_type) (nested_type_identifier)] @inherit.implements))

(extends_type_clause
  type: [(type_identifier) (generic_type) (nested_type_identifier)] @inherit.extends)
