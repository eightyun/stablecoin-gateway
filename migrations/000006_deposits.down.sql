DROP TRIGGER deposit_event_matches_immutable_guard ON deposit_event_matches;
DROP FUNCTION protect_deposit_event_match();
DROP TABLE deposit_event_matches;
DROP TABLE deposit_intents;
DROP TABLE deposit_addresses;
DROP INDEX chain_events_deposit_match_idx;
ALTER TABLE chain_events DROP CONSTRAINT chain_events_block_time_required;
ALTER TABLE chain_events DROP COLUMN block_time;
