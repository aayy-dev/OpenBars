"""Writes the unsigned Open Bars shortcuts (XML plists).

usage: make_shortcuts.py OUT_DIR [BUNDLE_ID TEAM_ID], then sign each with
  shortcuts sign --mode anyone --input X.unsigned.shortcut --output X.shortcut
  - "Open Bars Hotspot": Set Personal Hotspot On, or Off when its input is
    "off" (Open Bars passes that for “Stop Hotspot + Sharing”)
  - "Share with Open Bars": Set Personal Hotspot (On) + Open Bars › Start Sharing
    + Open App (Open Bars), so it ends in the app instead of a notification

Action formats were read from a shortcut made in Shortcuts on iOS 27.
"""
import plistlib
import sys
import uuid

out = sys.argv[1]
# The app that “Share with Open Bars” runs. Pass your own when you build Open
# Bars under your team (the bundled copy is for the published bundle ID).
BUNDLE = sys.argv[2] if len(sys.argv) > 2 else "dev.aayy.openbars"
TEAM = sys.argv[3] if len(sys.argv) > 3 else "R6J89TN4QR"

def hotspot(on):
    # Off is OnValue 0, as Shortcuts stores Set Cellular Data Off (same family).
    return {
        "WFWorkflowActionIdentifier": "is.workflow.actions.personalhotspot.set",
        "WFWorkflowActionParameters": {} if on else {"OnValue": 0},
    }


def if_input_is(text, then, otherwise):
    """If Shortcut Input is <text> (WFCondition 4 = “is”) then … otherwise …."""
    group = str(uuid.uuid4()).upper()
    def ctl(mode, extra=None):
        p = {"GroupingIdentifier": group, "WFControlFlowMode": mode}
        p.update(extra or {})
        return {"WFWorkflowActionIdentifier": "is.workflow.actions.conditional", "WFWorkflowActionParameters": p}
    start = ctl(0, {
        "WFCondition": 4,
        "WFConditionalActionString": text,
        # Shortcut Input “as Text”: without the coercion the If editor only
        # offers has / does not have any value, and “is” shows as invalid.
        "WFInput": {"Type": "Variable", "Variable": {
            "Value": {"Type": "ExtensionInput", "Aggrandizements": [
                {"Type": "WFCoercionVariableAggrandizement", "CoercionItemClass": "WFStringContentItem"}]},
            "WFSerializationType": "WFTextTokenAttachment"}},
    })
    end = ctl(2, {"UUID": str(uuid.uuid4()).upper()})
    return [start, *then, ctl(1), *otherwise, end]


HOTSPOT_ON = {
    # Defaults: operation "set", state On.
    "WFWorkflowActionIdentifier": "is.workflow.actions.personalhotspot.set",
    "WFWorkflowActionParameters": {},
}


def shortcut(name, actions, glyph, color, takes_text=False):
    d = {
        "WFWorkflowActions": actions,
        "WFWorkflowClientVersion": "2700.0.4",
        "WFWorkflowHasOutputFallback": False,
        "WFWorkflowIcon": {"WFWorkflowIconGlyphNumber": glyph, "WFWorkflowIconStartColor": color},
        "WFWorkflowImportQuestions": [],
        "WFWorkflowMinimumClientVersion": 900,
        "WFWorkflowMinimumClientVersionString": "900",
        "WFWorkflowName": name,
        "WFWorkflowOutputContentItemClasses": [],
        "WFWorkflowTypes": [],
    }
    if takes_text:
        d["WFWorkflowInputContentItemClasses"] = ["WFStringContentItem"]
    return d


def open_bars_action(intent):
    """An Open Bars App Intent action (e.g. StartSharingIntent)."""
    return {
        "WFWorkflowActionIdentifier": f"{BUNDLE}.{intent}",
        "WFWorkflowActionParameters": {
            "AppIntentDescriptor": {
                "AppIntentIdentifier": intent,
                "BundleIdentifier": BUNDLE,
                "Name": "Open Bars",
                "TeamIdentifier": TEAM,
            },
            "UUID": str(uuid.uuid4()).upper(),
        },
    }


OPEN_APP = {
    "WFWorkflowActionIdentifier": "is.workflow.actions.openapp",
    "WFWorkflowActionParameters": {
        "WFAppIdentifier": BUNDLE,
        "WFSelectedApp": {"BundleIdentifier": BUNDLE, "Name": "Open Bars", "TeamIdentifier": TEAM},
    },
}

GREEN = 4292093695  # Shortcuts' green
GLYPH = 61440       # antenna/wireless-style glyph

with open(f"{out}/Open Bars Hotspot.unsigned.shortcut", "wb") as f:
    plistlib.dump(shortcut("Open Bars Hotspot", if_input_is("off", [hotspot(False)], [hotspot(True)]),
                           GLYPH, GREEN, takes_text=True), f)
print("wrote Open Bars Hotspot")

with open(f"{out}/Share with Open Bars.unsigned.shortcut", "wb") as f:
    plistlib.dump(shortcut("Share with Open Bars", [HOTSPOT_ON, open_bars_action("StartSharingIntent"), OPEN_APP], GLYPH, GREEN), f)
print("wrote Share with Open Bars")
