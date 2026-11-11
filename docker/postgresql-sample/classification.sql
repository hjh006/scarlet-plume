CREATE TABLE IF NOT EXISTS inventory.classification_demo (
    id bigint PRIMARY KEY,
    data_a text NOT NULL,
    data_b text,
    data_c text,
    data_d text,
    fixture_kind text NOT NULL DEFAULT 'synthetic'
);

COMMENT ON TABLE inventory.classification_demo IS '赤翎自动分类、整改复测的合成样例';
COMMENT ON COLUMN inventory.classification_demo.data_a IS '合成样例字段 A';
COMMENT ON COLUMN inventory.classification_demo.data_b IS '虚构地区编码与校验位组成的合成样例';
COMMENT ON COLUMN inventory.classification_demo.data_c IS '合成样例字段 C';
COMMENT ON COLUMN inventory.classification_demo.data_d IS '支付测试号码，仅用于格式识别样例';

INSERT INTO inventory.classification_demo (id, data_a, data_b, data_c, data_d)
VALUES
    (1, 'person1@example.invalid', '990000199001010017', '13800000000', '4000000000000002'),
    (2, 'person2@example.invalid', NULL, '13900000000', NULL)
ON CONFLICT (id) DO NOTHING;

CREATE TABLE IF NOT EXISTS inventory.classification_contacts (
    id bigint PRIMARY KEY,
    contact_value text NOT NULL,
    fixture_kind text NOT NULL DEFAULT 'synthetic'
);
COMMENT ON TABLE inventory.classification_contacts IS '赤翎自动属性分类的合成联系信息样例';
INSERT INTO inventory.classification_contacts (id, contact_value)
VALUES (1, 'person3@example.invalid'), (2, 'person4@example.invalid')
ON CONFLICT (id) DO NOTHING;

GRANT SELECT ON TABLE inventory.customers, inventory.orders, inventory.shipments,
    inventory.classification_demo, inventory.classification_contacts TO sample_reader;

ANALYZE inventory.customers;
ANALYZE inventory.orders;
ANALYZE inventory.shipments;
ANALYZE inventory.classification_demo;
ANALYZE inventory.classification_contacts;
