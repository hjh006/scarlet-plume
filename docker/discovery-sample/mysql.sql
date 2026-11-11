CREATE TABLE discovery_sample.customers (
 id bigint PRIMARY KEY COMMENT '合成客户编号',
 email varchar(128) COMMENT '合成电子邮箱',
 department varchar(100) COMMENT '业务所属部门',
 phone varchar(32) COMMENT '合成手机号'
) COMMENT='仅用于数据发现验收的合成客户';
INSERT INTO discovery_sample.customers VALUES (1,'alice@example.invalid','operations','13800000000'),(2,'bob@example.invalid','engineering','13900000000');
REVOKE ALL PRIVILEGES, GRANT OPTION FROM 'discovery_reader'@'%';
GRANT SELECT, SHOW VIEW ON discovery_sample.* TO 'discovery_reader'@'%';
