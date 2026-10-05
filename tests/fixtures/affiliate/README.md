# Shopee product response evidence

`shopee-products-2026-10-05.json` contains ten product offer responses captured from the application's authenticated Chromium session, paired with the rendered price and commission rows on Shopee Affiliate. It contains only product fields; cookies, account details, tracking links and unrelated response fields were removed.

For every sample, `batch_item_for_item_card_full.price / 100000` matches the displayed VND price. The commission amounts are already formatted in VND (`₫2.375` means 2,375 VND) and commission percentages use a decimal comma. Keep the amount supplied by Shopee: rounded seller and platform amounts can sum to one VND more than the displayed total.

These captures verify the observed product schema and units. They are historical regression fixtures, not current product prices or proof of affiliate tracking or approved cashback.
