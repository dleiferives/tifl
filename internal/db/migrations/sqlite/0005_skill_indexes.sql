-- Reverse lookup for issue #66's skill-association repository methods.
CREATE INDEX idx_item_skill_assoc_skill
    ON item_skill_associations(skill_id, item_id);
