; graphmaster extraction query for C# (tree-sitter-c-sharp v0.23).
;
; Capture convention: see queries/go.scm. C# specifics:
;   - namespaces are captured as @def.namespace only so that the engine
;     nests their members; lang_csharp.go folds them into Def.Scope and
;     removes them (file-scoped namespaces apply to every later type);
;   - qualified names (A.B.C) and member access keep their left side as
;     @receiver, never @qualifier: whether A.B is a namespace, a type or a
;     value is decided by the resolver;
;   - base-list entries are captured as implements; lang_csharp.go turns
;     the base class of a class/record into extends;
;   - "global" / "static" using modifiers are recorded as import names.

; ---------------------------------------------------------------- imports

(using_directive
  "global"? @import.name
  "static"? @import.name
  !name
  [(identifier) (qualified_name) (generic_name) (alias_qualified_name)] @import.path) @import.wildcard

(using_directive
  "global"? @import.name
  name: (identifier) @import.alias
  .
  "="
  .
  (_) @import.path)

; ---------------------------------------------------------------- namespaces

(namespace_declaration
  name: (_) @name) @def.namespace

(file_scoped_namespace_declaration
  name: (_) @name) @def.namespace

; ---------------------------------------------------------------- types

(class_declaration
  name: (identifier) @name) @def.class

(interface_declaration
  name: (identifier) @name) @def.interface

(struct_declaration
  name: (identifier) @name) @def.struct

; "record struct" is turned into a struct by lang_csharp.go.
(record_declaration
  name: (identifier) @name) @def.class

(enum_declaration
  name: (identifier) @name) @def.enum

(enum_member_declaration
  name: (identifier) @name) @def.enum_member

(delegate_declaration
  name: (identifier) @name) @def.type

; ---------------------------------------------------------------- members

(method_declaration
  name: (identifier) @name) @def.method

(constructor_declaration
  name: (identifier) @name) @def.constructor

(local_function_statement
  name: (identifier) @name) @def.function

(property_declaration
  name: (identifier) @name) @def.property

(event_declaration
  name: (identifier) @name) @def.property

; const fields are turned into constants by lang_csharp.go.
(field_declaration
  (variable_declaration
    (variable_declarator
      name: (identifier) @name))) @def.field

(event_field_declaration
  (variable_declaration
    (variable_declarator
      name: (identifier) @name))) @def.field

; ---------------------------------------------------------------- tests

(method_declaration
  (attribute_list
    (attribute
      name: [(identifier) (qualified_name)] @_attr))
  (#match? @_attr "^([A-Za-z_][A-Za-z0-9_]*\\.)*(Test|Fact|Theory|TestMethod|DataTestMethod|TestCase|TestCaseSource)(Attribute)?$")) @test

; ---------------------------------------------------------------- locals

(type_parameter_list
  (type_parameter
    name: (identifier) @local))

; ---------------------------------------------------------------- calls

(invocation_expression
  function: [
    (identifier) @name
    (generic_name (identifier) @name)
  ]) @ref.call

(invocation_expression
  function: (member_access_expression
    expression: _ @receiver
    name: [
      (identifier) @name
      (generic_name (identifier) @name)
    ])) @ref.call

(invocation_expression
  function: (conditional_access_expression
    condition: _ @receiver
    (member_binding_expression
      name: [
        (identifier) @name
        (generic_name (identifier) @name)
      ]))) @ref.call

; Object creation is a constructor call of the created type.
(object_creation_expression
  type: [
    (identifier) @name
    (generic_name (identifier) @name)
  ]) @ref.call

(object_creation_expression
  type: (qualified_name
    qualifier: (_) @receiver
    name: [
      (identifier) @name
      (generic_name (identifier) @name)
    ])) @ref.call

; ---------------------------------------------------------------- members

(member_access_expression
  expression: _ @receiver
  name: [
    (identifier) @name
    (generic_name (identifier) @name)
  ]) @ref.member

(conditional_access_expression
  condition: _ @receiver
  (member_binding_expression
    name: [
      (identifier) @name
      (generic_name (identifier) @name)
    ])) @ref.member

; ---------------------------------------------------------------- types

(_
  type: [
    (identifier) @name
    (generic_name (identifier) @name)
  ]) @ref.type

(_
  type: (qualified_name
    qualifier: (_) @receiver
    name: [
      (identifier) @name
      (generic_name (identifier) @name)
    ])) @ref.type

(method_declaration
  returns: [
    (identifier) @name
    (generic_name (identifier) @name)
  ]) @ref.type

(method_declaration
  returns: (qualified_name
    qualifier: (_) @receiver
    name: [
      (identifier) @name
      (generic_name (identifier) @name)
    ])) @ref.type

(type_argument_list
  [
    (identifier) @name
    (generic_name (identifier) @name)
  ] @ref.type)

(type_argument_list
  (qualified_name
    qualifier: (_) @receiver
    name: [
      (identifier) @name
      (generic_name (identifier) @name)
    ]) @ref.type)

([(as_expression right: (identifier) @name)
  (is_expression right: (identifier) @name)
  (constant_pattern (identifier) @name)]) @ref.type

; ---------------------------------------------------------------- inheritance

(class_declaration
  (base_list
    [(identifier) (qualified_name) (generic_name) (alias_qualified_name)] @inherit.implements))

(record_declaration
  (base_list
    [(identifier) (qualified_name) (generic_name) (alias_qualified_name)] @inherit.implements))

(record_declaration
  (base_list
    (primary_constructor_base_type
      type: (_) @inherit.extends)))

(class_declaration
  (base_list
    (primary_constructor_base_type
      type: (_) @inherit.extends)))

(struct_declaration
  (base_list
    [(identifier) (qualified_name) (generic_name) (alias_qualified_name)] @inherit.implements))

(interface_declaration
  (base_list
    [(identifier) (qualified_name) (generic_name) (alias_qualified_name)] @inherit.extends))
