package validator

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/lkeix/gg-parser/query"
	"github.com/lkeix/gg-parser/schema"
)

type Validator struct {
	Schema *schema.Schema
	queryParser *query.Parser
}

func NewValidator(schema *schema.Schema, queryParser *query.Parser) *Validator {
	return &Validator{
		Schema: schema,
		queryParser: queryParser,
	}
}

func (v *Validator) Validate(q []byte) error {
	doc, err := v.queryParser.Parse(q)
	if err != nil {
		return err
	}

	if err := v.validateOperations(doc); err != nil {
		return fmt.Errorf("error validating operations: %w", err)
	}

	return nil
}

func (v *Validator) validateOperations(doc *query.Document) error {
	queryOperation := doc.Operations.GetQuery()
	schemaQuery := v.Schema.GetQuery()
	fragmentDefinitions := doc.FragmentDefinitions

	if err := validateField(schemaQuery, queryOperation, fragmentDefinitions, v.Schema.Indexes); err != nil {
		return err
	}

	return nil
}

func validateField(schemaOperation *schema.OperationDefinition, queryOperation *query.Operation, fragmentDefinitions query.FragmentDefinitions, indexes *schema.Indexes) error {
	if schemaOperation == nil {
		return errors.New("schema does not have a query operation")
	}

	if queryOperation == nil {
		return errors.New("query does not have a query operation")
	}

	if err := validateRootField(schemaOperation, queryOperation, fragmentDefinitions, indexes); err != nil {
		return err
	}

	return nil
}

func validateRootField(schemaOperation *schema.OperationDefinition, queryOperation *query.Operation, fragmentDefinitions query.FragmentDefinitions, indexes *schema.Indexes) error {
	for _, sel := range queryOperation.Selections {
		if field, ok := sel.(*query.Field); ok {
			f := schemaOperation.GetFieldByName(field.Name)
			if f == nil {
				return fmt.Errorf("field %s is not defined in schema", field.Name)
			}

			if err := validateFieldArguments(f.Arguments, field.Arguments); err != nil {
				return fmt.Errorf("error validating field %s: %w", field.Name, err)
			}

			premitiveFieldType := f.Type.GetPremitiveType()
			td := indexes.GetTypeDefinition(string(premitiveFieldType.Name))
			ud := indexes.GetUnionDefinition(string(premitiveFieldType.Name))
			id := indexes.GetInterfaceDefinition(string(premitiveFieldType.Name))
			if td == nil && ud == nil && id == nil {
				return nil
			}

			if td != nil {
				if err := validateSubField(td, field, fragmentDefinitions, indexes); err != nil {
					return fmt.Errorf("error validating field %s: %w", field.Name, err)
				}
			}

			if ud != nil {
				if len(field.GetSelections()) == 0 {
					return fmt.Errorf("union type %s must have subfields", ud.TypeName())
				}

				for _, sd := range ud.Types {
					t := indexes.GetTypeDefinition(string(sd))
					if err := validateSubField(t, field, fragmentDefinitions, indexes); err != nil {
						return fmt.Errorf("error validating field %s: %w", field.Name, err)
					}
				}
			}

			if id != nil {
				implementedTypes := indexes.GetImplementedType(id)
				for _, td := range implementedTypes {
					if err := validateSubField(td, field, fragmentDefinitions, indexes); err != nil {
						return fmt.Errorf("error validating field %s: %w", field.Name, err)
					}
				}
			}
		}
	}

	return nil
}

func validateFieldArguments(schemaArguments schema.ArgumentDefinitions, queryArguments []*query.Argument) error {
	if len(schemaArguments) == 0 && len(queryArguments) == 0 {
		return nil
	}

	requireds := schemaArguments.RequiredArguments()
	for _, queryArg := range queryArguments {
		for arg := range requireds {
			if bytes.Equal(queryArg.Name, arg.Name) {
				delete(requireds, arg)
			}
		}
	}

	if len(requireds) > 0 {
		args := make([]string, 0, len(requireds))
		for arg := range requireds {
			args = append(args, string(arg.Name))
		}

		return fmt.Errorf("missing required arguments: %v", args)
	}

	return nil
}

func validateSubField(t schema.CompositeType, field query.Selection, fragmentDefinitions query.FragmentDefinitions, indexes *schema.Indexes) error {
	fieldValidator := func(f *query.Field) error {
		schemaField := t.GetFieldByName(f.Name)
		if schemaField == nil {
			return fmt.Errorf("field %s is not defined in schema", f.Name)
		}

		if schemaField.Type.IsList {
			premitiveFieldType := schemaField.Type.GetPremitiveType()
			t := indexes.GetTypeDefinition(string(premitiveFieldType.Name))
			if t == nil {
				return nil
			}

			if err := validateSubField(t, f, fragmentDefinitions, indexes); err != nil {
				return fmt.Errorf("error validating field %s: %w", f.Name, err)
			}
		}
		
		return nil
	}

	fragmentValidator := func(f *query.FragmentSpread) error {
		fd := fragmentDefinitions.GetFragment(f.Name)
		if fd == nil {
			return fmt.Errorf("fragment %s is not defined", f.Name)
		}

		td := indexes.GetTypeDefinition(string(fd.BasedTypeName))
		id := indexes.GetInterfaceDefinition(string(fd.BasedTypeName))
		ud := indexes.GetUnionDefinition(string(fd.BasedTypeName))

		if td == nil && id == nil && ud == nil {
			return fmt.Errorf("type %s is not defined in schema", fd.BasedTypeName)
		}

		if !bytes.Equal(fd.BasedTypeName, t.TypeName()) {
			return fmt.Errorf("fragment %s is based on type %s, but field is of type %s", f.Name, fd.BasedTypeName, t.TypeName())
		}

		if err := validateSubField(t, fd, fragmentDefinitions, indexes); err != nil {
			return fmt.Errorf("error validating fragment %s: %w", f.Name, err)
		}

		return nil
	}

	inlineFragmentValidator := func(f *query.InlineFragment) error {
		td := indexes.GetTypeDefinition(string(f.TypeCondition))
		id := indexes.GetInterfaceDefinition(string(f.TypeCondition))
		ud := indexes.GetUnionDefinition(string(f.TypeCondition))

		if td == nil && id == nil && ud == nil {
			return fmt.Errorf("type %s is not defined in schema", f.TypeCondition)
		}

		if td != nil {
			if err := validateSubField(td, f, fragmentDefinitions, indexes); err != nil {
				return err
			}
		}

		if id != nil {
			if err := validateSubField(id, f, fragmentDefinitions, indexes); err != nil {
				return err
			}
		}

		if ud != nil {
			if err := validateSubField(ud, f, fragmentDefinitions, indexes); err != nil {
				return err
			}
		}

		return nil
	}

	for _, sel := range field.GetSelections()	{
		switch f := sel.(type) {
			case *query.Field:
				if err := fieldValidator(f); err != nil {
					return err
				}
			case *query.FragmentSpread:
				if err := fragmentValidator(f); err != nil {
					return err
				}
			case *query.InlineFragment:
				if err := inlineFragmentValidator(f); err != nil {
					return err
				}
		}
	}

	return nil
}
