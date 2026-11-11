// Local-only setup. Credentials and upstream JWTs are written only to ignored files.
import fs from 'node:fs';
import path from 'node:path';
import { generateKeyPairSync, randomBytes } from 'node:crypto';
import { parseEnv } from 'node:util';

const root = path.resolve(import.meta.dirname, '../..');
const envPath = path.join(root, '.env.scarlet-openmetadata');
const configDir = path.join(root, '.scarlet-openmetadata');
const env = fs.existsSync(envPath) ? parseEnv(fs.readFileSync(envPath, 'utf8')) : {};
const save = () => { fs.writeFileSync(envPath, Object.entries(env).map(([k, v]) => `${k}=${JSON.stringify(String(v))}`).join('\n') + '\n', { mode: 0o600 }); fs.chmodSync(envPath, 0o600); };
env.SCARLET_OM_DB_PASSWORD ||= randomBytes(24).toString('hex');
env.SCARLET_OM_ADMIN_PASSWORD ||= 'Scarlet!' + randomBytes(24).toString('hex');
env.SCARLET_OM_FERNET_KEY ||= randomBytes(32).toString('base64url') + '=';
save();
fs.mkdirSync(configDir, { recursive: true, mode: 0o700 });
if (!fs.existsSync(path.join(configDir, 'private_key.der'))) {
  const keys = generateKeyPairSync('rsa', { modulusLength: 2048, publicKeyEncoding: { type: 'spki', format: 'der' }, privateKeyEncoding: { type: 'pkcs8', format: 'der' } });
  fs.writeFileSync(path.join(configDir, 'private_key.der'), keys.privateKey, { mode: 0o600 });
  fs.writeFileSync(path.join(configDir, 'public_key.der'), keys.publicKey, { mode: 0o600 });
}
if (process.argv.includes('--init')) {
  console.log('Initialized local credentials and per-instance signing keys. Start OpenMetadata, then run this command again.');
  process.exit(0);
}

const origin = 'http://127.0.0.1:8585';
let adminToken;
async function request(url, method = 'GET', data, token = adminToken, expected = 200) {
  const response = await fetch(url, { method, redirect: 'error', headers: { 'Content-Type': method === 'PATCH' ? 'application/json-patch+json' : 'application/json', ...(token ? { Authorization: `Bearer ${token}` } : {}) }, body: data === undefined ? undefined : JSON.stringify(data), signal: AbortSignal.timeout(15000) });
  const raw = await response.text();
  if (![expected].flat().includes(response.status)) throw new Error(`${method} ${new URL(url).pathname}: HTTP ${response.status}`);
  if (!raw) return null;
  try { return JSON.parse(raw); } catch { return raw; }
}
async function om(route, method, data, token, expected) { return request(origin + '/api/v1' + route, method, data, token, expected); }
async function login(password) { return om('/users/login', 'POST', { email: 'admin@open-metadata.org', password: Buffer.from(password).toString('base64') }, null); }

async function main() {
  let signedIn;
  try { signedIn = await login(env.SCARLET_OM_ADMIN_PASSWORD); }
  catch {
    signedIn = await login('admin');
    adminToken = signedIn.accessToken;
    await om('/users/changePassword', 'PUT', { oldPassword: 'admin', newPassword: env.SCARLET_OM_ADMIN_PASSWORD, confirmPassword: env.SCARLET_OM_ADMIN_PASSWORD, requestType: 'SELF' });
    signedIn = await login(env.SCARLET_OM_ADMIN_PASSWORD);
  }
  adminToken = signedIn.accessToken;
  if (!adminToken) throw new Error('OpenMetadata did not return an access token');
  await om('/policies', 'PUT', { name: 'ScarletMetadataReadPolicy', enabled: true, rules: [
    { name: 'ReadTables', effect: 'allow', operations: ['ViewBasic', 'ViewAll'], resources: ['table', 'lineage'] },
    { name: 'NoConnectionsOrAdministration', effect: 'deny', operations: ['All'], resources: ['databaseService', 'ingestionPipeline', 'user', 'bot', 'role', 'policy', 'team'] },
    { name: 'NoRowsOrQueries', effect: 'deny', operations: ['ViewSampleData', 'ViewQueries', 'ViewDataProfile', 'ViewTestCaseFailedRowsSample'], resources: ['table'] },
    { name: 'NoTableMutation', effect: 'deny', operations: ['Create', 'BulkCreate', 'Delete', 'EditAll', 'EditLineage', 'EditTags', 'EditDescription', 'EditSampleData', 'EditDataProfile', 'EditOwners', 'EditRole'], resources: ['table', 'lineage'] },
  ] }, undefined, [200, 201]);
  await om('/roles', 'PUT', { name: 'ScarletMetadataReadRole', policies: ['ScarletMetadataReadPolicy'] }, undefined, [200, 201]);
  const roles = (await om('/roles?limit=100')).data;
  for (const [name, roleName, key] of [['scarlet-ingestion', 'IngestionBotRole', 'SCARLET_OM_INGESTION_TOKEN'], ['scarlet-discovery', 'ScarletMetadataReadRole', 'SCARLET_OPENMETADATA_TOKEN']]) {
    const role = roles.find(r => r.name === roleName);
    if (!role) throw new Error(`Required upstream role missing: ${roleName}`);
    let user;
    try { user = await om('/users/name/' + name + '?fields=roles'); }
    catch { user = await om('/users', 'POST', { name, email: name + '@open-metadata.org', isBot: true, isAdmin: false, roles: [role.id], authenticationMechanism: { authType: 'JWT', config: { JWTTokenExpiry: '30' } } }, undefined, 201); }
    // Upstream automatically adds DefaultBotRole to every new bot. Its
    // connection-reading grants are inappropriate for a metadata-only reader.
    if (name === 'scarlet-discovery') await om('/users/' + user.id, 'PATCH', [{ op: 'replace', path: '/roles', value: [{ id: role.id, type: 'role' }] }]);
    if (env[key] && !process.argv.includes('--rotate')) continue;
    const issued = await om('/users/generateToken/' + user.id, 'PUT', { JWTTokenExpiry: '30' });
    if (!issued.JWTToken) throw new Error('Upstream bot token missing');
    env[key] = issued.JWTToken;
    env[key + '_EXPIRES_AT'] = issued.JWTTokenExpiresAt;
    save();
  }
  await om('/tables?database=ScarletPostgres.scarlet_sample&fields=columns,tags,tableConstraints&limit=1', 'GET', undefined, env.SCARLET_OPENMETADATA_TOKEN);
  await om('/services/databaseServices/name/ScarletPostgres?fields=connection', 'GET', undefined, env.SCARLET_OPENMETADATA_TOKEN, [403, 404]);
  await om('/services/databaseServices', 'POST', { name: 'scarlet-readonly-must-not-create', serviceType: 'Postgres', connection: { config: { type: 'Postgres', hostPort: 'sample-db:5432', username: 'sample_reader', database: 'scarlet_sample' } } }, env.SCARLET_OPENMETADATA_TOKEN, 403);
  // The platform receives source/task configuration only, never its upstream credentials.
  if (!env.SCARLET_OM_DISCOVERY_TOKEN) {
    const credentials = parseEnv(fs.readFileSync(path.join(root, '.env.scarlet-login'), 'utf8'));
    const platform = 'http://127.0.0.1:8890';
    const auth = await fetch(platform + '/api/v1/session', { method: 'POST', headers: { Origin: platform, 'Content-Type': 'application/json' }, body: JSON.stringify({ username: credentials.SCARLET_BOOTSTRAP_USER, password: credentials.SCARLET_BOOTSTRAP_PASSWORD }), signal: AbortSignal.timeout(15000) });
    if (!auth.ok) throw new Error(`Platform login: HTTP ${auth.status}`);
    const cookie = auth.headers.getSetCookie().map(c => c.split(';')[0]).join('; ');
    const result = await fetch(platform + '/api/v1/workspaces/local/discovery/sources', { method: 'POST', headers: { Origin: platform, Cookie: cookie, 'Content-Type': 'application/json' }, body: JSON.stringify({ source: 'scarlet-openmetadata-postgres', provider: 'openmetadata', system: 'postgresql', database: 'scarlet_sample', scopes: ['inventory'], service: 'ScarletPostgres', intervalMinutes: 5, enabled: true, revision: 0 }), signal: AbortSignal.timeout(15000) });
    if (!result.ok) throw new Error(`Platform source creation: HTTP ${result.status}`);
    const source = await result.json();
    env.SCARLET_OM_DISCOVERY_TOKEN = source.token;
    env.SCARLET_OM_DISCOVERY_SOURCE_ID = source.source.id;
    save();
  }
  console.log(JSON.stringify({ status: 'configured', sourceId: Number(env.SCARLET_OM_DISCOVERY_SOURCE_ID), ingestionRole: 'IngestionBotRole', discoveryRole: 'ScarletMetadataReadRole', tokenValidityDays: 30, secretsPrinted: false }));
}
main().catch(error => { console.error(error.message); process.exitCode = 1; });
