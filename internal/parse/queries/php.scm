; graphmaster extraction query for PHP (tree-sitter-php v0.25, LanguagePHP).
;
; Capture convention: see queries/go.scm. PHP specifics (lang_php.go):
;   - the namespace is the module identity (@package), so it is not a def;
;   - qualified names (A\B\c) are captured whole as @qualifier and resolved
;     to fully qualified form ("\A\B" + name) by postProcess, which applies
;     PHP's textual name resolution (namespace + use imports);
;   - static scopes (Foo::bar, parent::bar) are captured as @receiver;
;     postProcess turns class names into qualifiers and keeps
;     self/static/parent as receivers;
;   - every use clause is an @import.path; clauses of a group use
;     (use A\{B, C}) also capture the whole declaration as @import.alias so
;     postProcess can prepend the group prefix (and then clears the alias);
;   - require/include capture their argument expression; importSpec keeps
;     only literal paths (optionally relative to __DIR__).

; ---------------------------------------------------------------- package

(namespace_definition
  name: (namespace_name) @package)

; Namespace markers: every namespace declaration is also captured as an
; import with alias "namespace" (the keyword) so postProcess knows where
; each namespace starts; "{" marks a braced global namespace. Markers are
; removed from the final imports.
(namespace_definition
  "namespace" @import.alias
  name: (namespace_name) @import.path)

(namespace_definition
  "namespace" @import.alias
  !name
  body: (compound_statement "{" @import.path))

; ---------------------------------------------------------------- imports

(namespace_use_declaration
  (namespace_use_clause) @import.path)

(namespace_use_declaration
  body: (namespace_use_group
    (namespace_use_clause) @import.path)) @import.alias

(require_expression
  [(string) (encapsed_string) (binary_expression) (parenthesized_expression)] @import.path)
(require_once_expression
  [(string) (encapsed_string) (binary_expression) (parenthesized_expression)] @import.path)
(include_expression
  [(string) (encapsed_string) (binary_expression) (parenthesized_expression)] @import.path)
(include_once_expression
  [(string) (encapsed_string) (binary_expression) (parenthesized_expression)] @import.path)

; ---------------------------------------------------------------- definitions

(class_declaration
  name: (name) @name) @def.class

(interface_declaration
  name: (name) @name) @def.interface

(trait_declaration
  name: (name) @name) @def.trait

(enum_declaration
  name: (name) @name) @def.enum

(enum_case
  name: (name) @name) @def.enum_member

(function_definition
  name: (name) @name) @def.function

(method_declaration
  name: (name) @name
  (#not-match? @name "^(?i:__construct)$")) @def.method

(method_declaration
  name: (name) @name
  (#match? @name "^(?i:__construct)$")) @def.constructor

; One def per property element; siblings share the declaration span.
(property_declaration
  (property_element
    name: (variable_name (name) @name))) @def.property

(const_declaration
  (const_element (name) @name)) @def.constant

; ---------------------------------------------------------------- tests

; PHPUnit: test* methods of a class that extends something.
(class_declaration
  (base_clause)
  body: (declaration_list
    (method_declaration
      name: (name) @_name) @test)
  (#match? @_name "^test"))

; #[Test] / #[\PHPUnit\Framework\Attributes\Test]
(method_declaration
  attributes: (attribute_list
    (attribute_group
      (attribute [(name) (qualified_name)] @_attr)))
  (#match? @_attr "(^|\\\\)Test$")) @test

; /** @test */ docblock
(declaration_list
  (comment) @_doc
  .
  (method_declaration) @test
  (#match? @_doc "@test\\b"))

; ---------------------------------------------------------------- references

; Function calls: foo(), A\foo(), \A\foo(), namespace\foo().
(function_call_expression
  function: (name) @name) @ref.call

(function_call_expression
  function: [
    (qualified_name (name) @name)
    (relative_name (name) @name)
  ] @qualifier) @ref.call

; Method calls on values: $x->m(), $x?->m().
(member_call_expression
  object: (_) @receiver
  name: (name) @name) @ref.call

(nullsafe_member_call_expression
  object: (_) @receiver
  name: (name) @name) @ref.call

; Static calls: Foo::m(), parent::m(), self::m(), static::m().
(scoped_call_expression
  scope: (_) @receiver
  name: (name) @name) @ref.call

; Property and constant access.
(member_access_expression
  object: (_) @receiver
  name: (name) @name) @ref.member

(nullsafe_member_access_expression
  object: (_) @receiver
  name: (name) @name) @ref.member

(scoped_property_access_expression
  scope: (_) @receiver
  name: (variable_name (name) @name)) @ref.member

(class_constant_access_expression
  .
  (_) @receiver
  .
  (name) @name .) @ref.member

; Type usage: annotations, new, static scopes.
(named_type
  (name) @name) @ref.type

(named_type
  [
    (qualified_name (name) @name)
    (relative_name (name) @name)
  ] @qualifier) @ref.type

(object_creation_expression
  (name) @name) @ref.type

(object_creation_expression
  [
    (qualified_name (name) @name)
    (relative_name (name) @name)
  ] @qualifier) @ref.type

(scoped_call_expression
  scope: (name) @name) @ref.type

(scoped_call_expression
  scope: [
    (qualified_name (name) @name)
    (relative_name (name) @name)
  ] @qualifier) @ref.type

(scoped_property_access_expression
  scope: (name) @name) @ref.type

(class_constant_access_expression
  .
  (name) @name
  .
  (name)) @ref.type

(class_constant_access_expression
  .
  [
    (qualified_name (name) @name)
    (relative_name (name) @name)
  ] @qualifier
  .
  (name)) @ref.type

; ---------------------------------------------------------------- inheritance

(class_declaration
  (base_clause [(name) (qualified_name) (relative_name)] @inherit.extends))

(class_declaration
  (class_interface_clause [(name) (qualified_name) (relative_name)] @inherit.implements))

(interface_declaration
  (base_clause [(name) (qualified_name) (relative_name)] @inherit.extends))

(enum_declaration
  (class_interface_clause [(name) (qualified_name) (relative_name)] @inherit.implements))

; Trait use inside a class, trait or enum body.
(use_declaration
  [(name) (qualified_name) (relative_name)] @inherit.embeds)
