const $ = id => document.getElementById(id);
const ms = n => Number.isFinite(n) ? n.toFixed(1) + ' ms' : '—';
const methodLabel = method => ({ivf_fallback:'IVF + SEQ', 'ivf+seq':'IVF + SEQ', seq:'SEQ', ivf:'IVF', hnsw:'HNSW'})[method] || '—';
const cacheLabel = state => ({hit:'Hit', miss:'Miss', skipped:'No aplica', disabled:'Desactivado', unavailable:'No disponible', online:'En línea'})[state] || '—';
let stream = null, chosen = null, previewUrl = null, busy = false, opening = false;
let running = false, loopId = 0, sourceId = 0, tab = 'scan', facing = 'user';
let lastBox = null, mirrored = false, snapshotUrl = null, preparing = false, enrollmentSaved = false;
const pause = ms => new Promise(resolve => setTimeout(resolve, ms));
const ready = () => Boolean(chosen || (stream && $('video').videoWidth));

function message(text = '', error = false, target = 'message') {
  const el = $(target);
  el.textContent = text;
  el.hidden = !text;
  el.classList.toggle('error', error);
}
async function api(path, options) {
  let res;
  try { res = await fetch(path, options); }
  catch { throw new Error('No se pudo conectar con FaceLab. Comprueba la conexión e inténtalo de nuevo.'); }
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    const e = new Error(typeof data.detail === 'string' ? data.detail : Array.isArray(data.detail) ? 'La consulta tiene datos incompletos. Selecciona otra foto y vuelve a intentarlo.' : 'Error del servidor (' + res.status + ').');
    e.status = res.status;
    throw e;
  }
  return data;
}
function counts(data) {
  if (data.persons != null) $('persons').textContent = data.persons.toLocaleString('es');
  if (data.samples != null) $('samples').textContent = data.samples.toLocaleString('es');
  if (data.dim != null) $('dimensions').textContent = data.dim;
  if (Number.isFinite(data.threshold)) $('threshold').textContent = Math.round(data.threshold * 100) + '%';
  if (data.cache_connection) $('cache-connection').textContent = cacheLabel(data.cache_connection);
  if (data.cache_stats) cacheMetrics(data.cache_stats);
  $('connection').dataset.state = 'online';
  $('connection-text').textContent = 'En línea';
}
function cacheMetrics(stats) {
  $('cache-hits').textContent = stats.hit;
  $('cache-misses').textContent = stats.miss;
  $('cache-skipped').textContent = stats.skipped;
  $('cache-unavailable').textContent = stats.unavailable;
  $('cache-disabled').textContent = stats.disabled;
  $('cache-rate').textContent = Number.isFinite(stats.hit_rate) ? (100 * stats.hit_rate).toFixed(1) + '%' : '—';
  $('cache-ttl').textContent = stats.ttl_seconds + ' s';
}
async function status() {
  try {
    counts(await api('/api/status'));
    message('', false, 'metrics-message');
  } catch (e) {
    $('connection').dataset.state = 'offline';
    $('connection-text').textContent = 'Sin conexión';
    message(e.message, true, tab === 'metrics' ? 'metrics-message' : 'message');
  }
}
function buttons() {
  $('recognize').disabled = busy || opening || preparing || !ready() || running;
  $('enroll').disabled = busy || opening || preparing || enrollmentSaved || !ready() || !$('alias').value.trim();
  $('live').disabled = !running && (busy || opening || preparing || !stream || !ready());
  $('start').disabled = opening;
  $('camera').disabled = opening;
  $('switch').disabled = opening;
  $('live-text').textContent = running ? 'Detener' : 'Continuo';
  $('recognize-label').textContent = snapshotUrl ? 'Escanear de nuevo' : 'Escanear';
  $('camera-label').textContent = snapshotUrl ? 'Volver a cámara' : 'Cámara';
  $('live').classList.toggle('active', running);
  $('live').setAttribute('aria-pressed', String(running));
}
function resultState(state, title, label, detail, score = null) {
  $('result-card').dataset.state = state;
  $('identity').textContent = title;
  $('verdict').textContent = label;
  $('result-detail').textContent = detail;
  $('result-detail').hidden = !detail;
  $('result-icon').querySelector('use').setAttribute('href', state === 'match' ? '#i-check' : state === 'unknown' || state === 'error' ? '#i-close' : '#i-scan');
  $('similarity').hidden = !Number.isFinite(score);
  $('score').textContent = Number.isFinite(score) ? (score * 100).toFixed(1) + '%' : '—';
  $('meter').style.width = Math.max(0, Math.min(100, (score || 0) * 100)) + '%';
}
function resetResult() {
  resultState('idle', 'Sin escanear', 'RECONOCIMIENTO', 'El resultado aparecerá aquí.');
  $('enroll-result').hidden = true;
}
function clearFace() {
  lastBox = null;
  $('face-box').hidden = true;
}
function fitFaceBox(box, mediaWidth, mediaHeight, viewWidth, viewHeight, flip) {
  const scale = Math.min(viewWidth / mediaWidth, viewHeight / mediaHeight);
  const width = mediaWidth * scale, height = mediaHeight * scale;
  const dx = (viewWidth - width) / 2, dy = (viewHeight - height) / 2;
  let [x1, y1, x2, y2] = box;
  if (flip) [x1, x2] = [1 - x2, 1 - x1];
  return {left: dx + x1 * width, top: dy + y1 * height,
    width: (x2 - x1) * width, height: (y2 - y1) * height};
}
function placeFace() {
  if (!lastBox) return;
  // Nunca dibujar un resultado antiguo sobre un vídeo que sigue cambiando.
  if (!chosen && !snapshotUrl) return clearFace();
  const view = $('viewport');
  const media = chosen ? $('preview') : $('snapshot');
  const w = media.naturalWidth, h = media.naturalHeight;
  if (!media.complete || !w || !h || !view.clientWidth || !view.clientHeight) return;
  const el = $('face-box');
  const rect = fitFaceBox(lastBox, w, h, view.clientWidth, view.clientHeight, !chosen && mirrored);
  for (const [key, value] of Object.entries(rect)) el.style[key] = value + 'px';
  el.hidden = false;
}
function showFace(box, matched, label) {
  if (!chosen && !snapshotUrl) return clearFace();
  if (!Array.isArray(box) || box.length !== 4 || !box.every(Number.isFinite)) return clearFace();
  const normalized = box.map(v => Math.min(1, Math.max(0, v)));
  if (normalized[2] <= normalized[0] || normalized[3] <= normalized[1]) return clearFace();
  lastBox = normalized;
  $('face-box').dataset.state = matched ? 'matched' : 'detected';
  $('face-tag').textContent = label;
  placeFace();
}
function resumePreview() {
  enrollmentSaved = false;
  clearFace();
  $('snapshot').hidden = true;
  $('snapshot').removeAttribute('src');
  if (snapshotUrl) URL.revokeObjectURL(snapshotUrl);
  snapshotUrl = null;
  $('live-guide').hidden = !stream;
  if (stream) $('media-state').textContent = 'Cámara activa';
  buttons();
}
function freezeFrame(blob) {
  resumePreview();
  snapshotUrl = URL.createObjectURL(blob);
  $('snapshot').classList.toggle('mirrored', mirrored);
  $('snapshot').src = snapshotUrl;
  $('snapshot').hidden = false;
  $('live-guide').hidden = true;
  $('media-state').textContent = 'Fotograma analizado';
  buttons();
}
function processing(on, enrolling = false) {
  $('processing').hidden = !on;
  $('scan-beam').hidden = !on;
  $('processing-text').textContent = enrolling ? 'Guardando...' : 'Analizando...';
}
function endLoop() {
  running = false;
  loopId++;
  buttons();
}
function releaseSource() {
  preparing = false;
  sourceId++;
  endLoop();
  if (stream) stream.getTracks().forEach(track => track.stop());
  stream = null;
  resumePreview();
  $('video').srcObject = null;
  $('video').hidden = true;
  $('preview').hidden = true;
  $('preview').removeAttribute('src');
  if (previewUrl) URL.revokeObjectURL(previewUrl);
  previewUrl = null; chosen = null; mirrored = false;
  $('file').value = '';
  $('placeholder').hidden = false;
  $('view-top').hidden = true;
  processing(false);
  clearFace();
  resetResult();
  buttons();
}
function showSource(camera) {
  $('video').hidden = !camera;
  $('preview').hidden = camera;
  $('placeholder').hidden = true;
  $('view-top').hidden = false;
  $('switch').hidden = !camera;
  $('live-guide').hidden = !camera;
  $('media-state').textContent = camera ? 'Cámara activa' : 'Fotografía';
  buttons();
}
async function startCamera(switching = false) {
  if (opening || tab === 'metrics') return;
  if (stream && !switching) return;
  releaseSource(); message();
  if (switching) facing = facing === 'user' ? 'environment' : 'user';
  const id = sourceId;
  opening = true; buttons();
  try {
    if (!navigator.mediaDevices?.getUserMedia) throw new Error('La cámara requiere HTTPS o localhost. Puedes subir una foto.');
    const next = await navigator.mediaDevices.getUserMedia({video:{facingMode: {ideal:facing},width:{ideal:640},height:{ideal:480}},audio:false});
    if (id !== sourceId || tab === 'metrics') { next.getTracks().forEach(t => t.stop()); return; }
    stream = next;
    const track = stream.getVideoTracks()[0];
    mirrored = (track?.getSettings().facingMode || facing) === 'user';
    $('video').classList.toggle('mirrored', mirrored);
    $('video').srcObject = stream;
    showSource(true);
    track?.addEventListener('ended', () => { if (stream === next) { releaseSource(); message('La cámara se desconectó.', true); } });
    await $('video').play();
  } catch (e) {
    if (id === sourceId) {
      releaseSource();
      message(e.name === 'NotAllowedError' ? 'Permite el acceso a la cámara en tu navegador.' : e.name === 'NotFoundError' ? 'No se encontró una cámara.' : e.message, true);
    }
  } finally { opening = false; buttons(); }
}
$('start').onclick = () => startCamera();
$('camera').onclick = () => {
  if (snapshotUrl && !busy) { resumePreview(); resetResult(); message(); }
  else startCamera();
};
$('switch').onclick = () => startCamera(true);
$('stop').onclick = () => { releaseSource(); message(); };
$('upload').onclick = () => $('file').click();
function normalizedSize(width, height) {
  if (!Number.isFinite(width) || !Number.isFinite(height) || width < 1 || height < 1) throw new Error('Dimensiones de imagen inválidas.');
  const scale = Math.min(1, 1600 / Math.max(width, height));
  return [Math.max(1, Math.round(width * scale)), Math.max(1, Math.round(height * scale))];
}
async function normalizeInBrowser(file) {
  const url = URL.createObjectURL(file);
  try {
    const image = new Image();
    await new Promise((resolve, reject) => {
      image.onload = resolve;
      image.onerror = () => reject(new Error('Formato no compatible con el navegador.'));
      image.src = url;
    });
    const canvas = document.createElement('canvas');
    [canvas.width, canvas.height] = normalizedSize(image.naturalWidth, image.naturalHeight);
    const ctx = canvas.getContext('2d');
    if (!ctx) throw new Error('No se pudo preparar la imagen.');
    ctx.fillStyle = '#fff';
    ctx.fillRect(0, 0, canvas.width, canvas.height);
    ctx.drawImage(image, 0, 0, canvas.width, canvas.height);
    const result = await new Promise(resolve => canvas.toBlob(resolve, 'image/jpeg', .90));
    if (!result || result.type !== 'image/jpeg' || result.size === 0 || result.size > 5 * 1024 * 1024) throw new Error('No se pudo convertir la imagen.');
    return result;
  } finally { URL.revokeObjectURL(url); }
}
async function normalizeOnServer(file) {
  const form = new FormData();
  form.append('photo', file, file.name || 'photo');
  let res;
  try { res = await fetch('/api/normalize', {method:'POST', body:form}); }
  catch { throw new Error('Sin conexión para preparar la foto. Vuelve a intentarlo.'); }
  if (!res.ok) {
    const data = await res.json().catch(() => ({}));
    throw new Error(typeof data.detail === 'string' ? data.detail : 'No se pudo abrir la foto. Prueba con JPG, PNG o WebP.');
  }
  const blob = await res.blob();
  if (blob.type !== 'image/jpeg' || !blob.size || blob.size > 5 * 1024 * 1024) throw new Error('No se recibió una imagen válida.');
  return blob;
}
$('file').onchange = async () => {
  const file = $('file').files[0];
  if (!file) return;
  releaseSource(); message();
  if (!file.size || file.size > 15 * 1024 * 1024) return message('Elige una foto no vacía de hasta 15 MB.', true);
  const id = sourceId;
  preparing = true; buttons();
  $('processing').hidden = false;
  $('processing-text').textContent = 'Preparando foto...';
  try {
    let normalized;
    try { normalized = await normalizeInBrowser(file); }
    catch (e) { if (id !== sourceId) return; normalized = await normalizeOnServer(file); }
    if (id !== sourceId) return;
    chosen = normalized; previewUrl = URL.createObjectURL(normalized);
    $('preview').src = previewUrl;
    showSource(false);
  } catch (e) {
    if (id === sourceId) message(e.message, true);
  } finally {
    if (id === sourceId) { preparing = false; processing(false); buttons(); }
  }
};
$('preview').addEventListener('load', () => { placeFace(); buttons(); });
$('snapshot').addEventListener('load', placeFace);
$('preview').addEventListener('error', () => {
  if (chosen) { releaseSource(); message('No se pudo abrir la foto.', true); }
});
$('video').addEventListener('loadedmetadata', buttons);
$('video').addEventListener('resize', placeFace);
if (window.ResizeObserver) new ResizeObserver(placeFace).observe($('viewport'));
else window.addEventListener('resize', placeFace);

async function photo() {
  if (chosen) return chosen;
  const v = $('video');
  if (!stream || !v.videoWidth) throw new Error('Activa la cámara o selecciona una foto.');
  const c = $('canvas');
  [c.width, c.height] = normalizedSize(v.videoWidth, v.videoHeight);
  c.getContext('2d').drawImage(v, 0, 0, c.width, c.height);
  const blob = await new Promise(resolve => c.toBlob(resolve, 'image/jpeg', .85));
  if (!blob) throw new Error('No se pudo capturar la imagen.');
  return blob;
}
function recognizeResult(data) {
  resultState(data.matched ? 'match' : 'unknown', data.matched ? data.alias : 'Sin coincidencia',
    data.matched ? 'IDENTIFICADO' : 'NO RECONOCIDO',
    data.matched ? 'Rostro reconocido' : data.reason || (Number.isFinite(data.threshold) ? 'Similitud insuficiente (umbral: ' + Math.round(data.threshold * 100) + '%).' : 'No hay un perfil coincidente.'), data.score);
  showFace(data.face_box, data.matched, data.matched ? 'Identificado' : 'Rostro detectado');
  $('index').textContent = methodLabel(data.method || data.requested_method || $('method').value);
  $('knn').textContent = ms(data.search_ms);
  $('api-time').textContent = ms(data.http_ms);
  $('embedding-time').textContent = ms(data.embedding_ms);
  $('total-time').textContent = ms(data.total_ms);
  $('cache').textContent = cacheLabel(data.cache_state);
  $('metadata-time').textContent = ms(data.metadata_ms);
  if (data.cache_stats) cacheMetrics(data.cache_stats);
}
async function submit(enrolling = false, continuous = false) {
  if (busy || preparing) return;
  if (enrolling && enrollmentSaved) return;
  if (!ready()) { endLoop(); message('Selecciona una foto o activa la cámara.', true); return; }
  if (enrolling && !$('alias').value.trim()) return message('Escribe un nombre o alias.', true);
  const id = sourceId, mode = tab, currentLoop = loopId;
  const valid = () => id === sourceId && mode === tab && (!continuous || (running && currentLoop === loopId));
  busy = true; buttons(); message(); processing(true, enrolling);
  if (!continuous) clearFace();
  if (enrolling) $('enroll-result').hidden = true;
  try {
    const form = new FormData();
    const blob = await photo();
    if (!valid()) return;
    if (!chosen && !continuous) freezeFrame(blob);
    form.append('photo', blob, 'capture.jpg');
    // The action button explicitly acknowledges the short permission notice.
    form.append('consent', 'true');
    if (enrolling) form.append('alias', $('alias').value.trim());
    else form.append('method', $('method').value);
    if (!valid()) return;
    const data = await api(enrolling ? '/api/enroll' : '/api/recognize', {method:'POST',body:form});
    if (enrolling) counts(data); // Even if the user changed screens, the save may have succeeded.
    if (!valid()) return;
    if (enrolling) {
      enrollmentSaved = true;
      showFace(data.face_box, true, 'Muestra guardada');
      const n = data.person_samples, goal = data.recommended_samples || 4;
      $('enroll-result').textContent = Number.isInteger(n) ? (n >= goal ? `Muestra guardada · ${n} muestras registradas.` : `Muestra ${n} de ${goal} guardada. Agrega otra con un ángulo o iluminación diferente.`) : 'Muestra guardada correctamente.';
      $('enroll-result').hidden = false;
    } else {
      recognizeResult(data);
    }
  } catch (e) {
    if (!valid()) return;
    clearFace();
    const faceError = e.status === 422 && /rostro/i.test(e.message);
    if (!enrolling) resultState(faceError ? 'idle' : 'error', faceError ? 'Busca un rostro' : 'No se pudo escanear',
      faceError ? 'SIN DETECCIÓN' : 'ERROR', e.message);
    if (!continuous || !faceError) message(e.message, true);
    if (continuous && !faceError) endLoop();
  } finally { busy = false; processing(false); buttons(); }
}
$('recognize').onclick = () => submit();
$('enroll').onclick = () => submit(true);
$('alias').addEventListener('input', buttons);
$('alias').addEventListener('keydown', e => { if (e.key === 'Enter' && !$('enroll').disabled) $('enroll').click(); });
$('live').onclick = async () => {
  if (running) { endLoop(); clearFace(); return; }
  if (!stream) return message('Activa la cámara para el escaneo continuo.', true);
  resumePreview();
  const current = ++loopId;
  running = true; buttons();
  while (running && loopId === current && stream && tab === 'scan') {
    await submit(false, true);
    await pause(650);
  }
};
$('method').onchange = () => {
  $('method-chip').textContent = methodLabel($('method').value);
};
function navigate(next) {
  if (tab === next) return;
  if (preparing) releaseSource();
  endLoop(); sourceId++; resumePreview(); processing(false); resetResult(); message();
  if (next === 'metrics') releaseSource();
  tab = next;
  $('capture-view').hidden = next === 'metrics';
  $('metrics-view').hidden = next !== 'metrics';
  $('scan-panel').hidden = next !== 'scan';
  $('enroll-panel').hidden = next !== 'enroll';
  $('method-chip').hidden = next !== 'scan';
  $('page-title').textContent = next === 'enroll' ? 'Registrar rostro' : 'Escanear rostro';
  $('section-label').textContent = next === 'enroll' ? 'REGISTRO' : 'IDENTIFICACIÓN';
  $('placeholder').querySelector('h2').textContent = next === 'enroll' ? 'Captura una muestra' : 'Listo para escanear';
  document.querySelectorAll('[data-tab]').forEach(btn => {
    if (btn.dataset.tab === next) btn.setAttribute('aria-current', 'page');
    else btn.removeAttribute('aria-current');
  });
  if (next === 'metrics') status();
  window.scrollTo(0, 0);
  buttons();
}
document.querySelectorAll('[data-tab]').forEach(btn => btn.onclick = () => navigate(btn.dataset.tab));
$('refresh').onclick = status;
window.addEventListener('pagehide', releaseSource);
document.addEventListener('visibilitychange', () => { if (document.hidden) { endLoop(); clearFace(); } });
buttons(); status();
