; graphmaster extraction query for Java (tree-sitter-java v0.23).
;
; Capture convention: see queries/go.scm and internal/parse/engine.go.
; Java specifics handled in lang_java.go (postProcess):
;   - a receiver that looks like a type or package path (Strings, a.b.Cls)
;     becomes a qualifier;
;   - import Names are filled from the spec ("*" for on-demand imports).

; ---------------------------------------------------------------- package

(package_declaration [(identifier) (scoped_identifier)] @package)

; ---------------------------------------------------------------- imports

; On-demand: import a.b.*; / import static a.b.C.*;
(import_declaration
  [(identifier) (scoped_identifier)] @import.path
  (asterisk) @import.wildcard)

; Single-type and single-static: import a.b.C; / import static a.b.C.m;
(import_declaration
  [(identifier) (scoped_identifier)] @import.path .)

; ---------------------------------------------------------------- types

(class_declaration name: (identifier) @name) @def.class
(record_declaration name: (identifier) @name) @def.class
(interface_declaration name: (identifier) @name) @def.interface
(annotation_type_declaration name: (identifier) @name) @def.interface
(enum_declaration name: (identifier) @name) @def.enum

; ---------------------------------------------------------------- members

(method_declaration name: (identifier) @name) @def.method
(annotation_type_element_declaration name: (identifier) @name) @def.method
(constructor_declaration name: (identifier) @name) @def.constructor
(compact_constructor_declaration name: (identifier) @name) @def.constructor

(field_declaration
  declarator: (variable_declarator name: (identifier) @name)) @def.field

; Interface fields are implicitly public static final.
(constant_declaration
  declarator: (variable_declarator name: (identifier) @name)) @def.constant

(enum_constant name: (identifier) @name) @def.enum_member

; Record components are (final) fields with accessor methods.
(record_declaration
  parameters: (formal_parameters
    (formal_parameter name: (identifier) @name) @def.field))

; ---------------------------------------------------------------- tests

; JUnit 4/5 and friends: an annotated method.
(method_declaration
  (modifiers
    [(marker_annotation name: (_) @_ann)
     (annotation name: (_) @_ann)])
  (#match? @_ann "(^|[.])(Test|ParameterizedTest|RepeatedTest|TestFactory|TestTemplate)$")) @test

; JUnit 3: test* methods of a TestCase subclass.
(class_declaration
  superclass: (superclass [(type_identifier) (scoped_type_identifier)] @_sup)
  body: (class_body
    (method_declaration name: (identifier) @_name) @test)
  (#match? @_sup "(^|[.])TestCase$")
  (#match? @_name "^test"))

; ---------------------------------------------------------------- locals

; Type parameters shadow type references inside their declaration.
(type_parameter (type_identifier) @local)

; ---------------------------------------------------------------- references

(method_invocation
  object: (_) @receiver
  name: (identifier) @name) @ref.call

(method_invocation
  !object
  name: (identifier) @name) @ref.call

; Constructor calls: new T(...) references T as a call.
(object_creation_expression
  type: (type_identifier) @name) @ref.call

(object_creation_expression
  type: (generic_type . (type_identifier) @name)) @ref.call

(object_creation_expression
  type: (scoped_type_identifier
    . (_) @qualifier
    (type_identifier) @name .)) @ref.call

(object_creation_expression
  type: (generic_type
    (scoped_type_identifier
      . (_) @qualifier
      (type_identifier) @name .))) @ref.call

; Method references: recv::name (recv::new is not a named member).
(method_reference
  . (_) @receiver
  .
  (identifier) @name .) @ref.call

(field_access
  object: (_) @receiver
  field: (identifier) @name) @ref.member

; Annotations reference their annotation type.
(marker_annotation name: (identifier) @name) @ref.type
(annotation name: (identifier) @name) @ref.type
(marker_annotation
  name: (scoped_identifier scope: (_) @qualifier name: (identifier) @name)) @ref.type
(annotation
  name: (scoped_identifier scope: (_) @qualifier name: (identifier) @name)) @ref.type

(scoped_type_identifier
  . (_) @qualifier
  (type_identifier) @name .) @ref.type

(type_identifier) @name @ref.type

; ---------------------------------------------------------------- inheritance

(superclass
  [(type_identifier) (scoped_type_identifier) (generic_type)] @inherit.extends)

(super_interfaces
  (type_list
    [(type_identifier) (scoped_type_identifier) (generic_type)] @inherit.implements))

(extends_interfaces
  (type_list
    [(type_identifier) (scoped_type_identifier) (generic_type)] @inherit.extends))
