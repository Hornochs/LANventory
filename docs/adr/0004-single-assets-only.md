# 0004 – Single assets only, no quantity items

**Status:** accepted

## Context

Inventory tools often support quantities ("40 patch cables"). DGUV V3 inspections, repairs,
status and locations, however, apply to individual items.

## Decision

Every asset is one physical item with its own inventory number. There are no quantity or stock
fields and no consumables.

## Consequences

- The model stays simple; history, status and inspections are per item.
- Large lots of identical cheap items mean many rows (or being left out); the CSV import
  makes first entry practical.
- Consumables or bulk stock would need a different concept and a new decision.
