INSERT INTO cashback_policies(share_percent) VALUES (50);
INSERT INTO app_settings(settings) VALUES ('{"brand":"Hoàn Xu","coinExchangeEnabled":false,"supportEmail":"","maxDisplayPercent":null}');
INSERT INTO reward_policies VALUES ('checkin','{"daily":1,"milestones":{"3":2,"7":5,"14":10,"30":30}}');
INSERT INTO affiliate_channels(id,name,status) VALUES ('shopee','Shopee','not_configured'),('lazada','Lazada','demo'),('tiktok','TikTok Shop','demo'),('tiki','Tiki','demo');
INSERT INTO wallet_accounts(kind) VALUES ('system');
INSERT INTO gift_catalog(id,name,channel,cost) VALUES ('g1','Voucher Shopee 50K','shopee',35),('g2','Voucher Shopee 100K','shopee',90),('g3','Voucher Lazada 100K','lazada',90),('g4','Voucher Tiki 200K','tiki',150);
