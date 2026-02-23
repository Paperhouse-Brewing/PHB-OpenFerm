// SPDX-License-Identifier: Apache-2.0
// PHB Firmware (Arduino Mega)

#include <Arduino.h>
#include <ArduinoJson.h>

const int PUMP_PIN = 22;
const int VALVE_PINS[] = {24,26,28,30};
const int VALVE_COUNT = sizeof(VALVE_PINS)/sizeof(VALVE_PINS[0]);

bool pumpOn = false;
bool valveOpen[4] = {false,false,false,false};
unsigned long lastTelemetryMs = 0;
unsigned long lastCmdMs = 0;

void safeAll() {
  digitalWrite(PUMP_PIN, LOW);
  pumpOn = false;
  for (int i=0;i<VALVE_COUNT;i++){ digitalWrite(VALVE_PINS[i], LOW); valveOpen[i]=false; }
}

void setup() {
  Serial.begin(115200);
  pinMode(PUMP_PIN, OUTPUT);
  for (int i=0;i<VALVE_COUNT;i++) pinMode(VALVE_PINS[i], OUTPUT);
  safeAll();
}

int fvIndexFromId(const char* fv) {
  if (fv[0]=='f' && fv[1]=='v') {
    int n = atoi(fv+2);
    if (n>=1 && n<=VALVE_COUNT) return n-1;
  }
  return -1;
}

void handleCommand(JsonDocument& doc) {
  const char* t = doc["t"] | "";
  if (strcmp(t, "set_valve")==0) {
    const char* fv = doc["fv"] | "";
    const char* state = doc["state"] | "";
    int idx = fvIndexFromId(fv);
    if (idx>=0) {
      bool open = strcmp(state,"open")==0;
      digitalWrite(VALVE_PINS[idx], open ? HIGH : LOW);
      valveOpen[idx] = open;
      bool anyOpen=false; for(int i=0;i<VALVE_COUNT;i++) anyOpen |= valveOpen[i];
      digitalWrite(PUMP_PIN, anyOpen ? HIGH : LOW);
      pumpOn = anyOpen;
      StaticJsonDocument<128> ack;
      ack["t"]="ack"; ack["cmd"]="set_valve"; ack["fv"]=fv; ack["ok"]=true;
      serializeJson(ack, Serial); Serial.println();
    }
  } else if (strcmp(t, "set_pump")==0) {
    const char* state = doc["state"] | "";
    pumpOn = strcmp(state,"on")==0;
    digitalWrite(PUMP_PIN, pumpOn ? HIGH : LOW);
    StaticJsonDocument<128> ack;
    ack["t"]="ack"; ack["cmd"]="set_pump"; ack["ok"]=true;
    serializeJson(ack, Serial); Serial.println();
  } else if (strcmp(t, "ping")==0) {
    StaticJsonDocument<96> pong; pong["t"]="pong";
    serializeJson(pong, Serial); Serial.println();
  }
}

void loop() {
  if (Serial.available()) {
    String line = Serial.readStringUntil('\n');
    if (line.length()>0) {
      StaticJsonDocument<256> doc;
      DeserializationError err = deserializeJson(doc, line);
      if (!err) { handleCommand(doc); lastCmdMs = millis(); }
    }
  }
  unsigned long now = millis();
  if (now - lastTelemetryMs > 1000) {
    lastTelemetryMs = now;
    for (int i=0;i<VALVE_COUNT;i++) {
      StaticJsonDocument<256> t;
      char fv[8]; snprintf(fv, sizeof(fv), "fv%d", i+1);
      t["t"]="telemetry"; t["fv"]=fv;
      t["beerC"]= 19.5;   // TODO: real sensor
      t["jktC"]= -3.0;    // TODO: real sensor
      t["valve"] = valveOpen[i] ? "open":"closed";
      t["flow"]  = pumpOn ? 1 : 0;
      serializeJson(t, Serial); Serial.println();
    }
  }
  // if (now - lastCmdMs > 60000) { safeAll(); }
}
