You are a senior backend architect and systems designer.

I want you to design a comprehensive technical plan for building a framework that generates a fully functional API backend from YAML configuration files.

## Core Idea

The system should allow developers to define an entire application declaratively using YAML. This includes:

* Entities (data models with schema definitions)
* API endpoints (CRUD + custom logic)
* Database configuration (multiple connections supported)
* Migrations (auto-generated and applied)
* Business rules / hooks
* Background jobs / cron tasks
* Plugin system (extensible via YAML + code modules)
  - crons could be an extension 

The framework should parse YAML files and dynamically (or via code generation) create a working API server.

## Technical Preferences

* Primary language: Go (preferred), but you may suggest alternatives if justified
* Database: Should support at least PostgreSQL initially
* API style: REST (GraphQL optional as an extension)
* Config format: YAML (strict and validated)
* Emphasis on performance, modularity, and developer experience

## What I Want From You

Create a detailed, structured plan that includes:

### 1. High-Level Architecture

* Core components of the system
* How YAML is parsed and transformed into runtime behavior
* Code generation vs runtime reflection tradeoffs (and your recommendation)

### 2. YAML Specification Design

* Example YAML structure for:

  * Entities (fields, types, relations, validation)
  * Endpoints (routes, methods, handlers, auth)
  * Cron jobs / background tasks
  * Database connections
  * Plugin configuration
* Validation strategy (schema, versioning, error handling)

### 3. Entity System

* How entities map to database tables
* Handling relationships (1-1, 1-many, many-many)
  - @references(fk=?) as an idea
* Indexes, constraints, and validations
* Versioning / migrations strategy

### 4. API Generation

* How endpoints are created from YAML
* CRUD auto-generation vs custom handlers
* Middleware support (auth, logging, rate limiting)
* Request/response validation

### 5. Authorization System (RBAC + ABAC) — CRITICAL

Design a full policy system including:

RBAC (Role-Based Access Control)
  Roles, permissions, role hierarchies
  Mapping roles to endpoints and entity actions
ABAC (Attribute-Based Access Control)
  Policy rules based on attributes:
  user attributes (role, org, department, subscription tier)
  resource attributes (entity fields, ownership, state)
  request context (time, IP, region, device)
  Policy expression language (YAML-based or DSL)
Enforcement Model
  Where policies are evaluated (middleware, service layer, ORM layer)
  Caching strategies for policy evaluation
  Performance considerations
  Example YAML

Include sample RBAC + ABAC policy definitions in YAML.

### 6. Migration System

* How migrations are generated from schema changes
* Safe migration strategies
* Rollbacks and version tracking

### 7. Plugin System

* How plugins are defined and loaded
* Lifecycle hooks (before/after request, entity hooks, etc.)
* Isolation and safety considerations

### 8. Multi-Database Support

* How multiple connections are defined and used
* Routing entities to different databases
* Transaction handling

### 9. Background Jobs / Cron

* YAML definition format
* Execution engine design
* Retry, logging, and scheduling

### 10. Developer Experience

* CLI tool (init project, validate YAML, generate code, run server)
* Hot reload vs compile step
* Debugging support

### 11. Example End-to-End Flow

* Show how a sample YAML file becomes a running API
* Include step-by-step transformation

### 12. Risks & Tradeoffs

* Performance concerns
* Complexity vs flexibility
* Security implications (especially with dynamic execution)

### 13. Suggested Tech Stack

* Go libraries (routing, ORM, YAML parsing, migrations, cron)
* Optional tools if code generation is used

## Output Requirements

* Be concrete and specific (not vague)
* Include example YAML snippets
* Prefer pragmatic solutions over theoretical ones
* Make clear recommendations when multiple approaches exist

## Goal

The final output should be detailed enough that an experienced engineer could begin implementing this framework immediately.
