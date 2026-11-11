const sample = db.getSiblingDB('discovery_sample');
sample.createCollection('customers',{validator:{$jsonSchema:{bsonType:'object',properties:{email:{bsonType:'string',description:'合成邮箱'},profile:{bsonType:'object',description:'合成客户资料'}}}}});
sample.customers.insertMany([{email:'alice@example.invalid',profile:{department:'operations',phone:'13800000000'}},{email:'bob@example.invalid',profile:{department:'engineering',phone:'13900000000'}}]);
sample.createUser({user:'discovery_reader',pwd:process.env.MONGO_INITDB_ROOT_PASSWORD,roles:[{role:'read',db:'discovery_sample'}]});
