// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package db

import (
	"cmp"
	"fmt"
	"slices"
	"sync"

	"xorm.io/xorm/schemas"
)

var (
	cachedForeignKeyOrderedTables = sync.OnceValues(foreignKeyOrderedTables)
	cachedTableNameLookupOrder    = sync.OnceValues(tableNameLookupOrder)
)

// Create a list of database tables in their "foreign key order".  This order specifies the safe insertion order for
// records into tables, where earlier tables in the list are referenced by foreign keys that exist in tables later in
// the list.  This order can be used in reverse as a safe deletion order as well.
//
// An ordered list of tables is incompatible with tables that have self-referencing foreign keys and circular referenced
// foreign keys; however neither of those cases are in-use in Forgejo.
func calculateTableForeignKeyOrder(tables []*schemas.Table) ([]*schemas.Table, error) {
	remainingTables := slices.Clone(tables)

	// Create a lookup for each table that has a foreign key, and a slice of the tables that it references it.
	referencingTables := make(map[string][]string)
	for _, table := range remainingTables {
		tableName := table.Name
		for _, fk := range table.ForeignKeys {
			referencingTables[tableName] = append(referencingTables[tableName], fk.TargetTableName)
		}
	}

	orderedTables := make([]*schemas.Table, 0, len(remainingTables))

	for len(remainingTables) > 0 {
		nextGroup := make([]*schemas.Table, 0, len(remainingTables))

		for _, targetTable := range remainingTables {
			// Skip if this targetTable has foreign keys and the target table hasn't been created.
			slice, ok := referencingTables[targetTable.Name]
			if ok && len(slice) > 1 { // This table is still referencing an uncreated table
				continue
			}
			// This table's references are satisfied or it had none
			nextGroup = append(nextGroup, targetTable)
		}

		if len(nextGroup) == 0 {
			return nil, fmt.Errorf("calculateTableForeignKeyOrder: unable to figure out next table from remainingTables = %#v", remainingTables)
		}

		orderedTables = append(orderedTables, nextGroup...)

		// Cleanup between loops: remove each table in nextGroup from remainingTables, and remove their table names from
		// referencingTables as well.
		for _, doneTable := range nextGroup {
			remainingTables = slices.DeleteFunc(remainingTables, func(remainingTable *schemas.Table) bool {
				return remainingTable.Name == doneTable.Name
			})
			for referencingTable, referencedTables := range referencingTables {
				referencingTables[referencingTable] = slices.DeleteFunc(referencedTables, func(tableName string) bool {
					return tableName == doneTable.Name
				})
			}
		}
	}

	return orderedTables, nil
}

// Create a list of registered database tables in their "foreign key order", per calculateTableForeignKeyOrder.
func foreignKeyOrderedTables() ([]*schemas.Table, error) {
	schemaTables := make([]*schemas.Table, 0, len(tables))
	for _, tbl := range tables {
		table, err := TableInfo(tbl)
		if err != nil {
			return nil, fmt.Errorf("foreignKeyOrderedTables: failure to fetch schema table for bean %#v: %w", tbl, err)
		}
		schemaTables = append(schemaTables, table)
	}

	orderedTables, err := calculateTableForeignKeyOrder(schemaTables)
	if err != nil {
		return nil, err
	}

	return orderedTables, nil
}

// Create a map from each registered database table's name to its order in "foreign key order", per
// calculateTableForeignKeyOrder.
func tableNameLookupOrder() (map[string]int, error) {
	tables, err := cachedForeignKeyOrderedTables()
	if err != nil {
		return nil, err
	}

	lookupMap := make(map[string]int, len(tables))
	for i, table := range tables {
		lookupMap[table.Name] = i
	}

	return lookupMap, nil
}

// When used as a comparator function in `slices.SortFunc`, can sort a slice into the safe insertion order for records
// in tables, where earlier tables in the list are referenced by foreign keys that exist in tables later in the list.
func TableNameInsertionOrderSortFunc(table1, table2 string) int {
	lookupMap, err := cachedTableNameLookupOrder()
	if err != nil {
		panic(fmt.Sprintf("cachedTableNameLookupOrder failed: %#v", err))
	}

	// Since this is typically used by `slices.SortFunc` it can't return an error.  If a table is referenced that isn't
	// a registered model then it will be sorted at the beginning -- this case is used in models/gitea_migrations/test.
	val1, ok := lookupMap[table1]
	if !ok {
		val1 = -1
	}
	val2, ok := lookupMap[table2]
	if !ok {
		val2 = -1
	}

	return cmp.Compare(val1, val2)
}
