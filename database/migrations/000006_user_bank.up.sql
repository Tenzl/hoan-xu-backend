ALTER TABLE users ADD COLUMN bank_details text;
COMMENT ON COLUMN users.bank_details IS 'AES-GCM encrypted JSON: bank, account, holder. Private payout profile, separate from display name.';
