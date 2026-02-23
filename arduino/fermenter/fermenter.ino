// SPDX-License-Identifier: Apache-2.0
#include <Arduino.h>

struct Cmd {
  String type;
  String fv;
  String state;
};

struct MapItem { const char* fv; uint8_t pin; };
MapItem VALVES[] = {
  {"fv1", 22},
  {"fv2", 23},
  {"fv3", 24},
};

uint8_t pinForFV(const String& fv){
  for (auto &m : VALVES) if (fv == m.fv) return m.pin;
  return 255;
}

void setValve(const String& fv, bool open){
  uint8_t pin = pinForFV(fv);
  if (pin == 255) return;
  digitalWrite(pin, open ? HIGH : LOW);
}

String readLine(){
  static String buf;
  while (Serial.available()) {
    char c = (char)Serial.read();
    if (c == '\n') { String out = buf; buf = ""; return out; }
    if (c != '\r') buf += c;
  }
  return String();
}

void sendTelemetry(const char* fv, float beerC){
  Serial.print("{\"type\":\"telemetry\",\"fv\":\""); Serial.print(fv);
  Serial.print("\",\"beer_c\":"); Serial.print(beerC, 2); Serial.println("}");
}

void sendPong(){
  Serial.println("{\"type\":\"pong\"}");
}

void setup(){
  Serial.begin(115200);
  for (auto &m : VALVES) { pinMode(m.pin, OUTPUT); digitalWrite(m.pin, LOW); }
}

unsigned long lastTele = 0;

void loop(){
  // handle incoming JSON (very naive parser)
  String line = readLine();
  if (line.length()) {
    // poor-man parsing; for production use ArduinoJson
    if (line.indexOf("\"type\":\"set_valve\"") >= 0) {
      int ifv = line.indexOf("\"fv\":\""); int ist = line.indexOf("\"state\":\"");
      if (ifv >= 0 && ist >= 0) {
        String fv = line.substring(ifv+6); fv = fv.substring(0, fv.indexOf('\"'));
        String st = line.substring(ist+9); st = st.substring(0, st.indexOf('\"'));
        setValve(fv, st == "open");
      }
    } else if (line.indexOf("\"type\":\"ping\"") >= 0) {
      sendPong();
    }
  }

  // TODO: read actual sensors here (per-FV probes) and send their temps
  // demo: fake a sinusoid for fv1
  if (millis() - lastTele > 1000) {
    lastTele = millis();
    static float x = 0;
    float fakeC = 18.0 + 0.5 * sinf(x); x += 0.1;
    sendTelemetry("fv1", fakeC);
  }
}
