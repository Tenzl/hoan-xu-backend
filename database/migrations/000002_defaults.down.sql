DELETE FROM gift_catalog;
DELETE FROM reward_policies;
DELETE FROM app_settings;
-- Financial accounts and policies are deliberately retained on rollback.
