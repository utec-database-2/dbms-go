import { useEffect, useState } from "react";

import L from "leaflet";
import "leaflet/dist/leaflet.css";
import {
  MapContainer,
  TileLayer,
  Circle,
  CircleMarker,
  Popup,
  useMap,
} from "react-leaflet";
import { Map as MapIcon, Search } from "lucide-react";

import { searchSpatialKNN, searchSpatialRange } from "../api";

const DEFAULT_CENTER = [-12.0432, -77.0282];

// Encuadra el mapa en cada búsqueda nueva (resetKey cambia en cada una). Si la
// consulta tiene un alcance (extentMeters > 0, el radio o la distancia al
// vecino más lejano) se ajusta el zoom para que todo el círculo quede a la
// vista; si no, se centra en el punto con zoom fijo.
function MapUpdater({ center, extentMeters, resetKey }) {
  const map = useMap();
  const [lat, lon] = center;

  useEffect(() => {
    if (extentMeters > 0) {
      // toBounds recibe el lado del cuadrado en metros (el diámetro del círculo).
      const bounds = L.latLng(lat, lon).toBounds(extentMeters * 2);
      map.fitBounds(bounds, { padding: [24, 24], maxZoom: 15 });
    } else {
      map.setView([lat, lon], 13);
    }

    // Recalcular el tamaño real del contenedor
    const timer = setTimeout(() => {
      map.invalidateSize();
    }, 100);

    return () => clearTimeout(timer);
  }, [map, lat, lon, extentMeters, resetKey]);

  useEffect(() => {
    const resizeObserver = new ResizeObserver(() => {
      map.invalidateSize();
    });

    resizeObserver.observe(map.getContainer());

    return () => {
      resizeObserver.disconnect();
    };
  }, [map]);

  return null;
}

function MapPanel() {
  const [latitude, setLatitude] = useState("-12.0432");
  const [longitude, setLongitude] = useState("-77.0282");
  const [radius, setRadius] = useState("5");
  const [metric, setMetric] = useState("haversine");

  // "range": todos los puntos dentro de un radio. "knn": los k más cercanos.
  const [mode, setMode] = useState("range");
  const [k, setK] = useState("10");

  // Se incrementa con cada búsqueda exitosa para que el mapa se vuelva a encuadrar.
  const [searchCount, setSearchCount] = useState(0);

  const [spatialResult, setSpatialResult] = useState(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState(null);

  const handleMetricChange = (event) => {
    const value = event.target.value;

    setMetric(value);

    // Valores iniciales cómodos para cada métrica.
    if (value === "haversine") {
      setRadius("5");
    } else {
      // En Euclidiana trabajamos directamente con lat/lon.
      setRadius("0.05");
    }
  };

  const handleSearch = async () => {
    setError(null);

    const lat = Number(latitude);
    const lon = Number(longitude);
    const radiusValue = Number(radius);
    const kValue = Number(k);

    if (!Number.isFinite(lat) || !Number.isFinite(lon)) {
      setError("La latitud y longitud deben ser números válidos.");
      return;
    }

    if (mode === "range" && (!Number.isFinite(radiusValue) || radiusValue < 0)) {
      setError("El radio debe ser un número mayor o igual a 0.");
      return;
    }

    if (mode === "knn" && (!Number.isInteger(kValue) || kValue < 1)) {
      setError("k debe ser un entero mayor o igual a 1.");
      return;
    }

    setLoading(true);

    try {
      const data =
        mode === "knn"
          ? await searchSpatialKNN({
              latitude: lat,
              longitude: lon,
              k: kValue,
              metric,
            })
          : await searchSpatialRange({
              latitude: lat,
              longitude: lon,
              radius: radiusValue,
              metric,
            });

      setSpatialResult(data);
      setSearchCount((n) => n + 1);
    } catch (err) {
      setError(err.message);
      setSpatialResult(null);
    } finally {
      setLoading(false);
    }
  };

  const mapCenter = spatialResult?.center
    ? [spatialResult.center.lat, spatialResult.center.lon]
    : DEFAULT_CENTER;

  const results = spatialResult?.results || [];

  /*
   * Leaflet dibuja Circle en metros.
   *
   * Haversine:
   *   radius está en km → multiplicamos por 1000.
   *
   * Euclidiana:
   *   radius está en grados de coordenadas.
   *   Usamos una conversión aproximada para visualizarlo.
   *
   * Se calcula con lo que devolvió el servidor (métrica y distancia del
   * resultado), no con los campos del formulario, que el usuario puede haber
   * cambiado después de buscar. En k-NN el círculo es el que alcanza al
   * vecino más lejano.
   */
  const resultMetric = spatialResult?.metric ?? metric;
  const resultExtent = spatialResult
    ? (spatialResult.maxDistance ?? spatialResult.radius ?? 0)
    : 0;
  const circleRadiusMeters =
    resultMetric === "haversine" ? resultExtent * 1000 : resultExtent * 111195;

  return (
    <section className="map-panel">
      <div className="map-panel-header">
        <div className="map-panel-title">
          <MapIcon size={19} />

          <div>
            <h2>Consulta espacial</h2>
            <p>
              {mode === "knn"
                ? "k vecinos más cercanos sobre el R-Tree"
                : "Consulta por rango sobre el R-Tree"}
            </p>
          </div>
        </div>

        <div className="map-result-count">
          {spatialResult ? `${results.length} resultado(s)` : "Sin consulta"}
        </div>
      </div>

      <div className="spatial-controls">
        <div className="spatial-field">
          <label>Consulta</label>

          <select value={mode} onChange={(e) => setMode(e.target.value)}>
            <option value="range">Por radio</option>
            <option value="knn">k vecinos (k-NN)</option>
          </select>
        </div>

        <div className="spatial-field">
          <label>Latitud</label>
          <input
            type="number"
            step="any"
            value={latitude}
            onChange={(e) => setLatitude(e.target.value)}
          />
        </div>

        <div className="spatial-field">
          <label>Longitud</label>
          <input
            type="number"
            step="any"
            value={longitude}
            onChange={(e) => setLongitude(e.target.value)}
          />
        </div>

        {mode === "range" ? (
          <div className="spatial-field">
            <label>Radio {metric === "haversine" ? "(km)" : "(grados)"}</label>

            <input
              type="number"
              min="0"
              step="any"
              value={radius}
              onChange={(e) => setRadius(e.target.value)}
            />
          </div>
        ) : (
          <div className="spatial-field">
            <label>k (vecinos)</label>

            <input
              type="number"
              min="1"
              step="1"
              value={k}
              onChange={(e) => setK(e.target.value)}
            />
          </div>
        )}

        <div className="spatial-field">
          <label>Métrica</label>

          <select value={metric} onChange={handleMetricChange}>
            <option value="haversine">Haversine</option>
            <option value="euclidean">Euclidiana</option>
          </select>
        </div>

        <button
          className="spatial-search-button"
          onClick={handleSearch}
          disabled={loading}
        >
          <Search size={16} />
          {loading ? "Buscando..." : "Buscar"}
        </button>
      </div>

      {error && <div className="spatial-error">{error}</div>}

      <div className="spatial-map-wrapper">
        <MapContainer
          center={DEFAULT_CENTER}
          zoom={13}
          scrollWheelZoom={true}
          className="spatial-map"
        >
          <MapUpdater
            center={mapCenter}
            extentMeters={circleRadiusMeters}
            resetKey={searchCount}
          />

          <TileLayer
            attribution='&copy; <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a>'
            url="https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png"
          />

          {/* Centro de la consulta */}
          <CircleMarker
            center={mapCenter}
            radius={9}
            pathOptions={{
              color: "#2563eb",
              fillColor: "#2563eb",
              fillOpacity: 0.9,
            }}
          >
            <Popup>
              <strong>Punto de consulta</strong>
              <br />
              Latitud: {mapCenter[0].toFixed(6)}
              <br />
              Longitud: {mapCenter[1].toFixed(6)}
            </Popup>
          </CircleMarker>

          {/* Radio de búsqueda */}
          {spatialResult && (
            <Circle
              center={mapCenter}
              radius={circleRadiusMeters}
              pathOptions={{
                color: "#2563eb",
                fillColor: "#2563eb",
                fillOpacity: 0.08,
              }}
            />
          )}

          {/* Resultados */}
          {results.map((point) => (
            <CircleMarker
              key={point.id}
              center={[point.lat, point.lon]}
              radius={7}
              pathOptions={{
                color: "#dc2626",
                fillColor: "#dc2626",
                fillOpacity: 0.8,
              }}
            >
              <Popup>
                <strong>
                  {point.rank ? `#${point.rank} · ` : ""}
                  {point.name}
                </strong>
                <br />
                Latitud: {point.lat.toFixed(6)}
                <br />
                Longitud: {point.lon.toFixed(6)}
                <br />
                Distancia:{" "}
                {resultMetric === "haversine"
                  ? `${point.distance.toFixed(2)} km`
                  : point.distance.toFixed(6)}
              </Popup>
            </CircleMarker>
          ))}
        </MapContainer>
      </div>
    </section>
  );
}

export default MapPanel;
